//! Memory operations share one embedding profile and one selected store.
//! No operation switches backends or retries an uncertain write.

use std::{num::NonZeroUsize, sync::Arc};

use rolio_core::{Confidence, Memory, MemoryId, MemoryKind, Scope, Source, Timestamp};
use thiserror::Error;

use crate::{
    embedding::{EmbeddingError, EmbeddingProvider, InputKind},
    store::{
        ListPage, ListRequest, MemoryStore, Revision, SearchHit, SearchRequest, StoreError,
        StoreIdentity, StoredMemory, Vector, WriteCondition, WriteRequest,
    },
};

/// A complete replacement of the editable fields. Omitted source means no
/// source, not "preserve the old value". Scope and times are not editable.
#[derive(Clone, Debug)]
pub struct MemoryInput {
    pub content: String,
    pub kind: MemoryKind,
    pub confidence: Confidence,
    pub source: Option<Source>,
}

impl MemoryInput {
    pub const MAX_CONTENT_BYTES: usize = 16 * 1024;
    pub const MAX_SOURCE_BYTES: usize = 4096;

    fn validate(&self) -> Result<(), MemoryError> {
        if self.content.trim().is_empty() || self.content.len() > Self::MAX_CONTENT_BYTES {
            return Err(MemoryError::InvalidInput);
        }
        if let Some(source) = &self.source {
            let uri = source.uri.as_deref().unwrap_or_default();
            let agent = source.agent.as_deref().unwrap_or_default();
            let reference = source.external_reference.as_deref().unwrap_or_default();
            // Source is descriptive data, not a URL to fetch or an instruction.
            // Preserve text exactly; size checks do not normalize or parse it.
            if uri.len() > 2048
                || agent.len() > 256
                || reference.len() > 2048
                || uri.len() + agent.len() + reference.len() > Self::MAX_SOURCE_BYTES
            {
                return Err(MemoryError::InvalidInput);
            }
        }
        Ok(())
    }
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum MemoryError {
    #[error("The memory or recall input exceeds the supported limits.")]
    InvalidInput,
    #[error("The memory does not exist.")]
    NotFound,
    #[error(transparent)]
    Store(#[from] StoreError),
    #[error(transparent)]
    Embedding(#[from] EmbeddingError),
}

pub struct MemoryService {
    store: Arc<dyn MemoryStore>,
    embedding: Arc<dyn EmbeddingProvider>,
    identity: StoreIdentity,
}

impl MemoryService {
    pub const MAX_RESULTS: usize = 100;
    pub const MAX_SCOPES: usize = 32;
    pub const MAX_QUERY_BYTES: usize = 8 * 1024;

    pub async fn new(
        store: Arc<dyn MemoryStore>,
        embedding: Arc<dyn EmbeddingProvider>,
        identity: StoreIdentity,
    ) -> Result<Self, MemoryError> {
        if embedding.profile() != &identity.embedding_profile {
            return Err(EmbeddingError::IncompatibleProfile.into());
        }
        store.check(&identity).await?;
        Ok(Self {
            store,
            embedding,
            identity,
        })
    }

    pub async fn check(&self) -> Result<(), MemoryError> {
        self.store.check(&self.identity).await?;
        Ok(())
    }

    async fn embed(&self, text: &str, kind: InputKind) -> Result<Vector, MemoryError> {
        let result = self.embedding.embed(text, kind).await?;
        if result.profile != self.identity.embedding_profile {
            return Err(EmbeddingError::IncompatibleProfile.into());
        }
        self.identity
            .embedding_profile
            .validate_vector(&result.vector)?;
        Ok(result.vector)
    }

    pub async fn remember(
        &self,
        id: MemoryId,
        scope: Scope,
        input: MemoryInput,
    ) -> Result<StoredMemory, MemoryError> {
        input.validate()?;
        self.check().await?;
        let vector = self.embed(&input.content, InputKind::Document).await?;
        let now = Timestamp::now_utc();
        let memory = Memory {
            id,
            scope,
            content: input.content,
            kind: input.kind,
            confidence: input.confidence,
            source: input.source,
            created_at: now,
            updated_at: now,
        };
        Ok(self
            .store
            .write(WriteRequest {
                memory,
                vector,
                condition: WriteCondition::Absent,
            })
            .await?)
    }

    pub async fn get(&self, id: MemoryId) -> Result<StoredMemory, MemoryError> {
        self.store.get(id).await?.ok_or(MemoryError::NotFound)
    }

    pub async fn update(
        &self,
        id: MemoryId,
        revision: Revision,
        input: MemoryInput,
    ) -> Result<StoredMemory, MemoryError> {
        input.validate()?;
        let previous = self.get(id).await?;
        if previous.revision != revision {
            return Err(StoreError::RevisionConflict.into());
        }
        let vector = if previous.memory.content == input.content {
            previous.vector
        } else {
            self.embed(&input.content, InputKind::Document).await?
        };
        // Do not move time backwards if the system clock was adjusted.
        let updated_at = Timestamp::now_utc().max(previous.memory.updated_at);
        let memory = Memory {
            content: input.content,
            kind: input.kind,
            confidence: input.confidence,
            source: input.source,
            updated_at,
            ..previous.memory
        };
        // The read above is only an early rejection. This final CAS is required:
        // another request can modify or delete the record during inference.
        Ok(self
            .store
            .write(WriteRequest {
                memory,
                vector,
                condition: WriteCondition::Revision(revision),
            })
            .await?)
    }

    pub async fn forget(&self, id: MemoryId, revision: Revision) -> Result<(), MemoryError> {
        self.store.delete(id, revision).await?;
        Ok(())
    }

    pub async fn list(&self, request: ListRequest) -> Result<ListPage, MemoryError> {
        if request.limit.get() > Self::MAX_RESULTS {
            return Err(MemoryError::InvalidInput);
        }
        Ok(self.store.list(request).await?)
    }

    /// Semantic similarity is primary; confidence only breaks equal scores.
    /// It is never a hard filter. ID is the final deterministic tie-breaker.
    /// Ranking applies to the backend's bounded candidate set, not the full DB.
    /// Kind filtering is not offered until the storage contract supports it.
    pub async fn recall(
        &self,
        query: &str,
        mut scopes: Vec<Scope>,
        limit: NonZeroUsize,
    ) -> Result<Vec<SearchHit>, MemoryError> {
        if query.trim().is_empty()
            || query.len() > Self::MAX_QUERY_BYTES
            || scopes.is_empty()
            || scopes.len() > Self::MAX_SCOPES
            || limit.get() > Self::MAX_RESULTS
        {
            return Err(MemoryError::InvalidInput);
        }
        scopes.sort();
        scopes.dedup();
        self.check().await?;
        let vector = self.embed(query, InputKind::Query).await?;
        let mut hits = self
            .store
            .search(SearchRequest {
                vector,
                scopes,
                limit,
            })
            .await?;
        // Reject invalid backend scores rather than rank NaN as meaningful.
        if hits.iter().any(|hit| !hit.score.is_finite()) {
            return Err(StoreError::Unavailable.into());
        }
        hits.sort_by(|left, right| {
            right
                .score
                .total_cmp(&left.score)
                .then_with(|| {
                    right
                        .record
                        .memory
                        .confidence
                        .cmp(&left.record.memory.confidence)
                })
                .then_with(|| left.record.memory.id.cmp(&right.record.memory.id))
        });
        hits.truncate(limit.get());
        Ok(hits)
    }
}

#[cfg(test)]
mod tests;
