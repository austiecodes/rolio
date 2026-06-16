//! Pinned Qwen3-Embedding-0.6B Q8_0 GGUF, single-item inference on llama.cpp.
//!
//! The GGUF carries the pooling mode (last token) for this model family; the
//! adapter still normalizes explicitly and validates shape and norm itself.

use std::{num::NonZeroU32, path::Path, time::Duration};

use llama_cpp_2::{
    context::params::LlamaContextParams,
    llama_backend::LlamaBackend,
    llama_batch::LlamaBatch,
    model::{AddBos, LlamaModel, params::LlamaModelParams},
};

use super::{BlockingModel, EmbeddingError, InputKind, LocalEmbedding, artifacts};
use crate::store::{DistanceMetric, EmbeddingProfile, Vector};

const MAX_TOKENS: usize = 4096;
const CONTEXT_TOKENS: u32 = 4352;
const DIMENSIONS: usize = 1024;
const MODEL_FILE: &str = "Qwen3-Embedding-0.6B-Q8_0.gguf";

impl LocalEmbedding {
    /// Blocking startup operation. Call before serving requests, or from a
    /// blocking startup task. No download, provider fallback, or GPU discovery.
    /// The supplied directory must contain the complete pinned artifact set.
    pub fn open(directory: &Path) -> Result<Self, EmbeddingError> {
        if !cfg!(all(target_os = "macos", target_arch = "aarch64")) {
            return Err(EmbeddingError::Unavailable);
        }
        let manifest = artifacts::verify(directory)?;
        let model_path = directory.join(MODEL_FILE);
        let profile = EmbeddingProfile {
            model: format!("{}@{}:Q8_0", manifest.model, manifest.revision),
            artifact_digest: artifacts::digest(),
            dimensions: DIMENSIONS.try_into().expect("nonzero model dimension"),
            distance: DistanceMetric::Cosine,
            document_preprocessing: "exact content; no prefix; eos appended; \
                 max 4096 tokens; reject overflow"
                .into(),
            query_preprocessing: "exact query; no prefix; eos appended; \
                 max 4096 tokens; reject overflow"
                .into(),
            pooling: "last token (from model metadata); single sequence; batch size 1".into(),
            normalization: "llama.cpp pooled output; explicit L2 normalize; f32".into(),
        };
        Self::start(
            move || {
                // The backend and the model live and die on the owner thread.
                let backend = LlamaBackend::init().map_err(|_| EmbeddingError::Unavailable)?;
                let model =
                    LlamaModel::load_from_file(&backend, &model_path, &LlamaModelParams::default())
                        .map_err(|_| EmbeddingError::Unavailable)?;
                if model.n_embd() as usize != DIMENSIONS {
                    return Err(EmbeddingError::IncompatibleProfile);
                }
                Ok(QwenModel { backend, model })
            },
            profile,
            8,
            Duration::from_secs(60),
        )
    }
}

struct QwenModel {
    backend: LlamaBackend,
    model: LlamaModel,
}

impl BlockingModel for QwenModel {
    fn infer(&mut self, text: &str, _kind: InputKind) -> Result<Vector, EmbeddingError> {
        // Overflow is rejected before inference, never truncated. The appended
        // eos token is the fixed final token that last-token pooling reads.
        let mut tokens = self
            .model
            .str_to_token(text, AddBos::Never)
            .map_err(|_| EmbeddingError::InvalidInput)?;
        tokens.push(self.model.token_eos());
        if tokens.len() > MAX_TOKENS {
            return Err(EmbeddingError::TokenLimit);
        }
        let params = LlamaContextParams::default()
            .with_embeddings(true)
            .with_n_ctx(NonZeroU32::new(CONTEXT_TOKENS))
            .with_n_batch(CONTEXT_TOKENS);
        let mut context = self
            .model
            .new_context(&self.backend, params)
            .map_err(|_| EmbeddingError::Unavailable)?;
        let mut batch = LlamaBatch::new(tokens.len(), 1);
        for (position, token) in tokens.iter().enumerate() {
            batch
                .add(*token, position as i32, &[0], false)
                .map_err(|_| EmbeddingError::InvalidInput)?;
        }
        context
            .decode(&mut batch)
            .map_err(|_| EmbeddingError::Unavailable)?;
        let raw = context
            .embeddings_seq_ith(0)
            .map_err(|_| EmbeddingError::Unavailable)?;
        if raw.len() != DIMENSIONS {
            return Err(EmbeddingError::InvalidVector);
        }
        let mut values = raw.to_vec();
        let squared: f64 = values.iter().map(|value| f64::from(*value).powi(2)).sum();
        if !squared.is_finite() || squared <= 0.0 {
            return Err(EmbeddingError::InvalidVector);
        }
        let scale = (squared.sqrt() as f32).recip();
        for value in &mut values {
            *value *= scale;
        }
        let norm: f64 = values.iter().map(|value| f64::from(*value).powi(2)).sum();
        if (norm - 1.0).abs() > 1e-4 {
            return Err(EmbeddingError::InvalidVector);
        }
        Vector::new(values).map_err(|_| EmbeddingError::InvalidVector)
    }
}

#[cfg(test)]
mod tests;
