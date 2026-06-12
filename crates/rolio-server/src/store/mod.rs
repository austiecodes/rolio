//! The server owns this contract; clients never open a storage backend.
//!
//! Each adapter must make a conditional write one protected operation. A successful
//! write is durable and publishes the memory, revision, and vector together. After
//! an acknowledged update or delete, new reads and searches must not return the
//! old record. Requests already in progress may return a complete earlier version.
//! An uncertain commit is not a normal rejection and must not trigger blind retry.

use std::{num::NonZeroUsize, str::FromStr};

use async_trait::async_trait;
use rolio_core::{Memory, MemoryId, Scope};
use thiserror::Error;
use uuid::Uuid;

/// An opaque concurrency token, separate from the business memory.
#[derive(Clone, Copy, Debug, Eq, Hash, PartialEq)]
pub struct Revision(Uuid);

impl Revision {
    pub fn new() -> Self {
        Self(Uuid::new_v4())
    }
}

impl Default for Revision {
    fn default() -> Self {
        Self::new()
    }
}

impl std::fmt::Display for Revision {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        self.0.fmt(f)
    }
}

impl FromStr for Revision {
    type Err = uuid::Error;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        value.parse().map(Self)
    }
}

/// A finite, nonzero dense vector suitable for cosine search.
#[derive(Clone, Debug, PartialEq)]
pub struct Vector(Vec<f32>);

impl Vector {
    pub fn new(values: Vec<f32>) -> Result<Self, StoreError> {
        if values.is_empty()
            || values.iter().any(|value| !value.is_finite())
            || values.iter().all(|value| *value == 0.0)
        {
            return Err(StoreError::InvalidVector);
        }
        Ok(Self(values))
    }

    pub fn as_slice(&self) -> &[f32] {
        &self.0
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum DistanceMetric {
    Cosine,
}

/// Exact identity of the model artifacts and transformations used by a store.
/// The artifact digest covers the model and tokenizer file manifest.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct EmbeddingProfile {
    pub model: String,
    pub artifact_digest: [u8; 32],
    pub dimensions: NonZeroUsize,
    pub distance: DistanceMetric,
    pub document_preprocessing: String,
    pub query_preprocessing: String,
    pub pooling: String,
    pub normalization: String,
}

impl EmbeddingProfile {
    pub fn validate_vector(&self, vector: &Vector) -> Result<(), StoreError> {
        if vector.as_slice().len() != self.dimensions.get() {
            return Err(StoreError::DimensionMismatch);
        }
        Ok(())
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StoreIdentity {
    pub schema_version: u32,
    pub embedding_profile: EmbeddingProfile,
}

/// Internal storage representation. Vectors are not public CLI output fields.
#[derive(Clone, Debug, PartialEq)]
pub struct StoredMemory {
    pub memory: Memory,
    pub revision: Revision,
    pub vector: Vector,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum WriteCondition {
    Absent,
    Revision(Revision),
}

#[derive(Clone, Debug)]
pub struct WriteRequest {
    pub memory: Memory,
    pub vector: Vector,
    pub condition: WriteCondition,
}

/// Internal cursor state. The HTTP boundary must encode this as an opaque token.
/// List order is always ascending memory ID; cursors are bound to one scope.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ListCursor {
    pub scope: Scope,
    pub after: MemoryId,
}

#[derive(Clone, Debug)]
pub struct ListRequest {
    pub scope: Scope,
    pub cursor: Option<ListCursor>,
    pub limit: NonZeroUsize,
}

impl ListRequest {
    pub fn validate(&self) -> Result<(), StoreError> {
        if self
            .cursor
            .as_ref()
            .is_some_and(|cursor| cursor.scope != self.scope)
        {
            return Err(StoreError::CursorMismatch);
        }
        Ok(())
    }
}

#[derive(Clone, Debug)]
pub struct ListPage {
    pub records: Vec<StoredMemory>,
    pub next: Option<ListCursor>,
}

#[derive(Clone, Debug)]
pub struct SearchRequest {
    pub vector: Vector,
    pub scopes: Vec<Scope>,
    pub limit: NonZeroUsize,
}

impl SearchRequest {
    pub fn validate(&self) -> Result<(), StoreError> {
        if self.scopes.is_empty() {
            return Err(StoreError::MissingScope);
        }
        Ok(())
    }
}

#[derive(Clone, Debug)]
pub struct SearchHit {
    pub record: StoredMemory,
    /// Higher is better, but values are not comparable across backends.
    /// Business ranking, including confidence, is applied outside the adapter.
    pub score: f32,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum StoreError {
    #[error("The memory ID already exists.")]
    AlreadyExists,
    #[error("The expected revision does not match the current record.")]
    RevisionConflict,
    #[error("An update cannot change the memory scope.")]
    ScopeChange,
    #[error("The memory is invalid.")]
    InvalidMemory,
    #[error("The vector must be finite, nonempty, and nonzero.")]
    InvalidVector,
    #[error("The vector dimension does not match the embedding profile.")]
    DimensionMismatch,
    #[error("Select at least one search scope.")]
    MissingScope,
    #[error("The cursor does not belong to this scope.")]
    CursorMismatch,
    #[error("The schema or embedding profile does not match the store.")]
    IncompatibleIdentity,
    #[error("The operation exceeds the adapter resource limit.")]
    ResourceLimit,
    #[error("The store is unavailable.")]
    Unavailable,
    #[error("The write outcome is unknown. Read back before another write.")]
    OutcomeUnknown,
}

#[async_trait]
pub trait MemoryStore: Send + Sync {
    async fn get(&self, id: MemoryId) -> Result<Option<StoredMemory>, StoreError>;

    /// Create or fully replace one record. No unconditional overwrite is allowed.
    /// The adapter generates a fresh revision on each successful write.
    async fn write(&self, request: WriteRequest) -> Result<StoredMemory, StoreError>;

    /// A missing record also fails the revision condition.
    async fn delete(&self, id: MemoryId, revision: Revision) -> Result<(), StoreError>;

    /// Stable keyset order, not a snapshot under concurrent writes. Adapters must
    /// report resource limits rather than silently return an incomplete export.
    async fn list(&self, request: ListRequest) -> Result<ListPage, StoreError>;

    /// Filter within the selected scopes, not after an unfiltered top-k query.
    /// Approximate retrieval need not produce exact top-k or identical scores.
    async fn search(&self, request: SearchRequest) -> Result<Vec<SearchHit>, StoreError>;

    /// Check availability and exact schema/profile identity without changing data.
    async fn check(&self, expected: &StoreIdentity) -> Result<(), StoreError>;
}

#[cfg(test)]
pub(crate) mod contracts;
#[cfg(test)]
pub(crate) mod fake;
