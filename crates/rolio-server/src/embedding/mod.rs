//! Local inference runs on one owner thread, never on the async executor.

#[cfg(any(test, feature = "embedding-local"))]
use std::time::Duration;

use async_trait::async_trait;
use thiserror::Error;
#[cfg(any(test, feature = "embedding-local"))]
use tokio::sync::{mpsc, oneshot};

use crate::store::{EmbeddingProfile, Vector};

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum InputKind {
    Document,
    Query,
}

#[derive(Clone, Debug)]
pub struct Embedding {
    pub profile: EmbeddingProfile,
    pub vector: Vector,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum EmbeddingError {
    #[error("The embedding input is empty or exceeds the byte limit.")]
    InvalidInput,
    #[error("The input exceeds the model token limit; shorten it before retrying.")]
    TokenLimit,
    #[error("The embedding worker queue is full.")]
    Busy,
    #[error("Embedding did not finish before the deadline.")]
    Timeout,
    #[error("The local embedding model is unavailable.")]
    Unavailable,
    #[error("The embedding artifacts or settings do not match the pinned profile.")]
    IncompatibleProfile,
    #[error("The model returned an invalid vector.")]
    InvalidVector,
}

#[async_trait]
pub trait EmbeddingProvider: Send + Sync {
    fn profile(&self) -> &EmbeddingProfile;
    async fn embed(&self, text: &str, kind: InputKind) -> Result<Embedding, EmbeddingError>;
}

/// A blocking implementation. Token checks must include prefixes and special
/// tokens and must reject overflow rather than truncate it.
#[cfg(any(test, feature = "embedding-local"))]
pub(crate) trait BlockingModel: Send + 'static {
    fn infer(&mut self, text: &str, kind: InputKind) -> Result<Vector, EmbeddingError>;
}

#[cfg(any(test, feature = "embedding-local"))]
#[derive(Debug)]
struct Job {
    text: String,
    kind: InputKind,
    reply: oneshot::Sender<Result<Vector, EmbeddingError>>,
}

/// One active inference and a bounded queue. Dropped queued jobs are skipped.
/// An active inference can finish after cancellation, but cannot write storage.
/// Dropping the provider closes the queue; the thread then drops the model.
#[cfg(any(test, feature = "embedding-local"))]
pub struct LocalEmbedding {
    sender: mpsc::Sender<Job>,
    profile: EmbeddingProfile,
    timeout: Duration,
}

#[cfg(any(test, feature = "embedding-local"))]
impl LocalEmbedding {
    pub const MAX_INPUT_BYTES: usize = 16 * 1024;

    /// The model is built on the owner thread itself: native handles are not
    /// required to be sendable, and a failed build must not abort the process.
    pub(crate) fn start<M, F>(
        build: F,
        profile: EmbeddingProfile,
        capacity: usize,
        timeout: Duration,
    ) -> Result<Self, EmbeddingError>
    where
        M: BlockingModel,
        F: FnOnce() -> Result<M, EmbeddingError> + Send + 'static,
    {
        if !(1..=32).contains(&capacity) || timeout.is_zero() || timeout > Duration::from_secs(300)
        {
            return Err(EmbeddingError::InvalidInput);
        }
        let (sender, mut receiver) = mpsc::channel::<Job>(capacity);
        std::thread::Builder::new()
            .name("rolio-embedding".into())
            .spawn(move || {
                let Ok(mut model) = build() else {
                    while let Some(job) = receiver.blocking_recv() {
                        let _ = job.reply.send(Err(EmbeddingError::Unavailable));
                    }
                    return;
                };
                while let Some(job) = receiver.blocking_recv() {
                    if !job.reply.is_closed() {
                        let result = model.infer(&job.text, job.kind);
                        let _ = job.reply.send(result);
                    }
                }
            })
            .map_err(|_| EmbeddingError::Unavailable)?;
        Ok(Self {
            sender,
            profile,
            timeout,
        })
    }
}

#[cfg(any(test, feature = "embedding-local"))]
#[async_trait]
impl EmbeddingProvider for LocalEmbedding {
    fn profile(&self) -> &EmbeddingProfile {
        &self.profile
    }

    async fn embed(&self, text: &str, kind: InputKind) -> Result<Embedding, EmbeddingError> {
        if text.trim().is_empty() || text.len() > Self::MAX_INPUT_BYTES {
            return Err(EmbeddingError::InvalidInput);
        }
        let (reply, receiver) = oneshot::channel();
        self.sender
            .try_send(Job {
                text: text.into(),
                kind,
                reply,
            })
            .map_err(|error| match error {
                mpsc::error::TrySendError::Full(_) => EmbeddingError::Busy,
                mpsc::error::TrySendError::Closed(_) => EmbeddingError::Unavailable,
            })?;
        let vector = tokio::time::timeout(self.timeout, receiver)
            .await
            .map_err(|_| EmbeddingError::Timeout)?
            .map_err(|_| EmbeddingError::Unavailable)??;
        self.profile
            .validate_vector(&vector)
            .map_err(|_| EmbeddingError::InvalidVector)?;
        Ok(Embedding {
            profile: self.profile.clone(),
            vector,
        })
    }
}

#[cfg(feature = "embedding-local")]
mod artifacts;
#[cfg(feature = "embedding-local")]
mod local;

#[cfg(test)]
mod tests;
