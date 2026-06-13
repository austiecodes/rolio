//! PostgreSQL adapter: one contract, one independent first-party implementation.
//!
//! Creation is one atomic `INSERT ... ON CONFLICT DO NOTHING`. Replacement
//! takes the row lock first (`SELECT ... FOR UPDATE`) and re-evaluates the
//! revision and scope conditions under it, so an update can never resurrect a
//! deleted record. Vectors travel in the pgvector text protocol; the adapter
//! owns the codec, so the SQL layer does not depend on a vector crate.
//! Filtered search enables iterative scan when pgvector 0.8+ is installed, so
//! low-hit scope filters do not under-return.

use std::{num::NonZeroUsize, time::Duration};

use async_trait::async_trait;
use rolio_core::{Confidence, Memory, MemoryId, MemoryKind, Scope};
use sha2::{Digest, Sha256};
use sqlx::{
    Row,
    postgres::{PgPool, PgPoolOptions, PgRow},
};
use time::OffsetDateTime;
use uuid::Uuid;

use super::{
    DistanceMetric, EmbeddingProfile, ListCursor, ListPage, ListRequest, MemoryStore, Revision,
    SearchHit, SearchRequest, StoreError, StoreIdentity, StoredMemory, Vector, WriteCondition,
    WriteRequest,
};

/// Columns of one memory record, with the vector rendered as text.
const RECORD_COLUMNS: &str = "id, scope, kind, content, confidence, source_uri, source_agent, \
     source_external, created_at, updated_at, revision, content_hash, \
     embedding::text AS embedding_text";

pub struct PgStore {
    pool: PgPool,
    identity: StoreIdentity,
    iterative_scan: bool,
}

impl PgStore {
    /// Connect, apply migrations for the given profile, and bind the store
    /// identity. A database initialized with a different identity is rejected;
    /// switching profiles is an explicit export and rebuild operation.
    pub async fn connect(url: &str, identity: StoreIdentity) -> Result<Self, StoreError> {
        let pool = PgPoolOptions::new()
            .max_connections(8)
            .acquire_timeout(Duration::from_secs(5))
            .connect(url)
            .await
            .map_err(|_| StoreError::Unavailable)?;
        let store = Self {
            iterative_scan: pgvector_supports_iterative_scan(&pool).await,
            pool,
            identity: identity.clone(),
        };
        store
            .apply_migrations(identity.embedding_profile.dimensions.get())
            .await?;
        store.bind_identity(identity).await?;
        Ok(store)
    }

    /// Idempotent bootstrap. Runs once per database at startup.
    async fn apply_migrations(&self, dimensions: usize) -> Result<(), StoreError> {
        let statements = [
            "CREATE EXTENSION IF NOT EXISTS vector".to_string(),
            format!(
                "CREATE TABLE IF NOT EXISTS memories (
                     id              UUID PRIMARY KEY,
                     scope           TEXT NOT NULL,
                     kind            TEXT NOT NULL,
                     content         TEXT NOT NULL,
                     confidence      SMALLINT NOT NULL CHECK (confidence BETWEEN 0 AND 10),
                     source_uri      TEXT,
                     source_agent    TEXT,
                     source_external TEXT,
                     created_at      TIMESTAMPTZ NOT NULL,
                     updated_at      TIMESTAMPTZ NOT NULL,
                     revision        UUID NOT NULL,
                     content_hash    TEXT NOT NULL,
                     embedding       vector({dimensions}) NOT NULL
                 )"
            ),
            String::from(
                "CREATE INDEX IF NOT EXISTS memories_scope_id_idx ON memories (scope, id)",
            ),
            String::from(
                "CREATE INDEX IF NOT EXISTS memories_embedding_hnsw_idx \
                 ON memories USING hnsw (embedding vector_cosine_ops)",
            ),
            String::from(
                "CREATE TABLE IF NOT EXISTS store_meta (
                     schema_version         INTEGER PRIMARY KEY,
                     model                  TEXT NOT NULL,
                     artifact_digest        TEXT NOT NULL,
                     dimensions             INTEGER NOT NULL,
                     distance               TEXT NOT NULL,
                     document_preprocessing TEXT NOT NULL,
                     query_preprocessing    TEXT NOT NULL,
                     pooling                TEXT NOT NULL,
                     normalization          TEXT NOT NULL,
                     initialized_at         TIMESTAMPTZ NOT NULL DEFAULT now()
                 )",
            ),
        ];
        for statement in statements {
            sqlx::query(sqlx::AssertSqlSafe(statement))
                .execute(&self.pool)
                .await
                .map_err(|_| StoreError::Unavailable)?;
        }
        Ok(())
    }

    async fn bind_identity(&self, identity: StoreIdentity) -> Result<(), StoreError> {
        sqlx::query(
            "INSERT INTO store_meta (
                 schema_version, model, artifact_digest, dimensions, distance,
                 document_preprocessing, query_preprocessing, pooling, normalization
             ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
             ON CONFLICT (schema_version) DO NOTHING",
        )
        .bind(identity.schema_version as i32)
        .bind(identity.embedding_profile.model.clone())
        .bind(hex(&identity.embedding_profile.artifact_digest))
        .bind(identity.embedding_profile.dimensions.get() as i32)
        .bind(distance_to_text(&identity.embedding_profile))
        .bind(identity.embedding_profile.document_preprocessing.clone())
        .bind(identity.embedding_profile.query_preprocessing.clone())
        .bind(identity.embedding_profile.pooling.clone())
        .bind(identity.embedding_profile.normalization.clone())
        .execute(&self.pool)
        .await
        .map_err(|_| StoreError::Unavailable)?;
        let stored = self.stored_identity(identity.schema_version).await?;
        if stored.as_ref() != Some(&identity) {
            return Err(StoreError::IncompatibleIdentity);
        }
        Ok(())
    }

    async fn stored_identity(
        &self,
        schema_version: u32,
    ) -> Result<Option<StoreIdentity>, StoreError> {
        let row = sqlx::query(
            "SELECT schema_version, model, artifact_digest, dimensions, distance, \
                 document_preprocessing, query_preprocessing, pooling, normalization \
             FROM store_meta WHERE schema_version = $1",
        )
        .bind(schema_version as i32)
        .fetch_optional(&self.pool)
        .await
        .map_err(|_| StoreError::Unavailable)?;
        let Some(row) = row else {
            return Ok(None);
        };
        let digest_text: String = row.try_get("artifact_digest").map_err(corrupt)?;
        let mut artifact_digest = [0_u8; 32];
        hex_decode(&digest_text, &mut artifact_digest).ok_or_else(|| corrupt(()))?;
        Ok(Some(StoreIdentity {
            schema_version: row.try_get::<i32, _>("schema_version").map_err(corrupt)? as u32,
            embedding_profile: EmbeddingProfile {
                model: row.try_get("model").map_err(corrupt)?,
                artifact_digest,
                dimensions: NonZeroUsize::new(
                    row.try_get::<i32, _>("dimensions").map_err(corrupt)? as usize,
                )
                .ok_or_else(|| corrupt(()))?,
                distance: distance_from_text(
                    &row.try_get::<String, _>("distance").map_err(corrupt)?,
                )?,
                document_preprocessing: row.try_get("document_preprocessing").map_err(corrupt)?,
                query_preprocessing: row.try_get("query_preprocessing").map_err(corrupt)?,
                pooling: row.try_get("pooling").map_err(corrupt)?,
                normalization: row.try_get("normalization").map_err(corrupt)?,
            },
        }))
    }
}

#[async_trait]
impl MemoryStore for PgStore {
    async fn get(&self, id: MemoryId) -> Result<Option<StoredMemory>, StoreError> {
        let row = sqlx::query(sqlx::AssertSqlSafe(format!(
            "SELECT {RECORD_COLUMNS} FROM memories WHERE id = $1"
        )))
        .bind(id.as_uuid())
        .fetch_optional(&self.pool)
        .await
        .map_err(|_| StoreError::Unavailable)?;
        row.map(|row| record_from_row(&row)).transpose()
    }

    async fn write(&self, request: WriteRequest) -> Result<StoredMemory, StoreError> {
        request
            .memory
            .validate()
            .map_err(|_| StoreError::InvalidMemory)?;
        self.identity
            .embedding_profile
            .validate_vector(&request.vector)?;
        let revision = Revision::new();
        let vector_text = vector_to_text(&request.vector);
        let hash = content_hash(&request.memory.content);
        let memory = &request.memory;
        match request.condition {
            // Create only when the ID is absent: one atomic statement.
            WriteCondition::Absent => {
                let row = sqlx::query(sqlx::AssertSqlSafe(format!(
                    "INSERT INTO memories (
                         id, scope, kind, content, confidence, source_uri, source_agent,
                         source_external, created_at, updated_at, revision, content_hash, embedding
                     ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::vector)
                     ON CONFLICT (id) DO NOTHING
                     RETURNING {RECORD_COLUMNS}"
                )))
                .bind(memory.id.as_uuid())
                .bind(scope_to_text(&memory.scope))
                .bind(kind_to_text(memory.kind))
                .bind(memory.content.clone())
                .bind(i16::from(memory.confidence.tenths()))
                .bind(memory.source.as_ref().and_then(|source| source.uri.clone()))
                .bind(
                    memory
                        .source
                        .as_ref()
                        .and_then(|source| source.agent.clone()),
                )
                .bind(
                    memory
                        .source
                        .as_ref()
                        .and_then(|source| source.external_reference.clone()),
                )
                .bind(memory.created_at)
                .bind(memory.updated_at)
                .bind(revision.as_uuid())
                .bind(hash)
                .bind(vector_text)
                .fetch_optional(&self.pool)
                .await
                .map_err(|_| StoreError::OutcomeUnknown)?;
                let Some(row) = row else {
                    return Err(StoreError::AlreadyExists);
                };
                record_from_row(&row)
            }
            // Replacement must never resurrect a deleted record, so the row
            // lock is taken first and the condition is re-evaluated under it.
            WriteCondition::Revision(expected) => {
                let mut transaction = self
                    .pool
                    .begin()
                    .await
                    .map_err(|_| StoreError::OutcomeUnknown)?;
                let current =
                    sqlx::query("SELECT revision, scope FROM memories WHERE id = $1 FOR UPDATE")
                        .bind(memory.id.as_uuid())
                        .fetch_optional(&mut *transaction)
                        .await
                        .map_err(|_| StoreError::OutcomeUnknown)?;
                let Some(current) = current else {
                    return Err(StoreError::RevisionConflict);
                };
                let current_revision: Uuid = current.try_get("revision").map_err(corrupt)?;
                let current_scope: String = current.try_get("scope").map_err(corrupt)?;
                if current_revision != expected.as_uuid() {
                    return Err(StoreError::RevisionConflict);
                }
                if current_scope != scope_to_text(&memory.scope) {
                    return Err(StoreError::ScopeChange);
                }
                let row = sqlx::query(sqlx::AssertSqlSafe(format!(
                    "UPDATE memories SET
                         scope = $2, kind = $3, content = $4, confidence = $5,
                         source_uri = $6, source_agent = $7, source_external = $8,
                         created_at = $9, updated_at = $10, revision = $11,
                         content_hash = $12, embedding = $13::vector
                     WHERE id = $1
                     RETURNING {RECORD_COLUMNS}"
                )))
                .bind(memory.id.as_uuid())
                .bind(scope_to_text(&memory.scope))
                .bind(kind_to_text(memory.kind))
                .bind(memory.content.clone())
                .bind(i16::from(memory.confidence.tenths()))
                .bind(memory.source.as_ref().and_then(|source| source.uri.clone()))
                .bind(
                    memory
                        .source
                        .as_ref()
                        .and_then(|source| source.agent.clone()),
                )
                .bind(
                    memory
                        .source
                        .as_ref()
                        .and_then(|source| source.external_reference.clone()),
                )
                .bind(memory.created_at)
                .bind(memory.updated_at)
                .bind(revision.as_uuid())
                .bind(hash)
                .bind(vector_text)
                .fetch_one(&mut *transaction)
                .await
                .map_err(|_| StoreError::OutcomeUnknown)?;
                transaction
                    .commit()
                    .await
                    .map_err(|_| StoreError::OutcomeUnknown)?;
                record_from_row(&row)
            }
        }
    }

    async fn delete(&self, id: MemoryId, revision: Revision) -> Result<(), StoreError> {
        let row = sqlx::query("DELETE FROM memories WHERE id = $1 AND revision = $2 RETURNING id")
            .bind(id.as_uuid())
            .bind(revision.as_uuid())
            .fetch_optional(&self.pool)
            .await
            .map_err(|_| StoreError::OutcomeUnknown)?;
        if row.is_some() {
            Ok(())
        } else {
            Err(StoreError::RevisionConflict)
        }
    }

    async fn list(&self, request: ListRequest) -> Result<ListPage, StoreError> {
        request.validate()?;
        let after = request.cursor.as_ref().map(|cursor| cursor.after.as_uuid());
        let fetch_limit = request.limit.get() as i64 + 1;
        let rows = sqlx::query(sqlx::AssertSqlSafe(format!(
            "SELECT {RECORD_COLUMNS} FROM memories \
             WHERE scope = $1 AND ($2::uuid IS NULL OR id > $2) \
             ORDER BY id ASC LIMIT $3"
        )))
        .bind(scope_to_text(&request.scope))
        .bind(after)
        .bind(fetch_limit)
        .fetch_all(&self.pool)
        .await
        .map_err(|_| StoreError::Unavailable)?;
        let has_more = rows.len() > request.limit.get();
        let mut records = Vec::with_capacity(request.limit.get());
        for row in rows.iter().take(request.limit.get()) {
            records.push(record_from_row(row)?);
        }
        let next = if has_more {
            records.last().map(|record| ListCursor {
                scope: request.scope.clone(),
                after: record.memory.id,
            })
        } else {
            None
        };
        Ok(ListPage { records, next })
    }

    async fn search(&self, request: SearchRequest) -> Result<Vec<SearchHit>, StoreError> {
        request.validate()?;
        self.identity
            .embedding_profile
            .validate_vector(&request.vector)?;
        let scopes: Vec<String> = request.scopes.iter().map(scope_to_text).collect();
        let vector_text = vector_to_text(&request.vector);
        let mut transaction = self
            .pool
            .begin()
            .await
            .map_err(|_| StoreError::Unavailable)?;
        if self.iterative_scan {
            sqlx::query("SET LOCAL hnsw.iterative_scan = strict_order")
                .execute(&mut *transaction)
                .await
                .map_err(|_| StoreError::Unavailable)?;
        }
        let rows = sqlx::query(sqlx::AssertSqlSafe(format!(
            "SELECT {RECORD_COLUMNS}, 1.0 - (embedding <=> $1::vector) AS score \
             FROM memories WHERE scope = ANY($2) \
             ORDER BY embedding <=> $1::vector LIMIT $3"
        )))
        .bind(&vector_text)
        .bind(&scopes)
        .bind(request.limit.get() as i64)
        .fetch_all(&mut *transaction)
        .await
        .map_err(|_| StoreError::Unavailable)?;
        transaction
            .commit()
            .await
            .map_err(|_| StoreError::Unavailable)?;
        let mut hits = Vec::with_capacity(rows.len());
        for row in rows {
            let score: f64 = row.try_get("score").map_err(corrupt)?;
            hits.push(SearchHit {
                record: record_from_row(&row)?,
                score: score as f32,
            });
        }
        Ok(hits)
    }

    async fn check(&self, expected: &StoreIdentity) -> Result<(), StoreError> {
        sqlx::query("SELECT 1")
            .execute(&self.pool)
            .await
            .map_err(|_| StoreError::Unavailable)?;
        let stored = self.stored_identity(expected.schema_version).await?;
        if stored.as_ref() == Some(expected) {
            Ok(())
        } else {
            Err(StoreError::IncompatibleIdentity)
        }
    }
}

async fn pgvector_supports_iterative_scan(pool: &PgPool) -> bool {
    let version = sqlx::query("SELECT extversion FROM pg_extension WHERE extname = 'vector'")
        .fetch_optional(pool)
        .await
        .ok()
        .flatten()
        .and_then(|row| row.try_get::<String, _>("extversion").ok());
    let Some(version) = version else {
        return false;
    };
    let mut parts = version.split('.');
    let major = parts.next().and_then(|part| part.parse::<u32>().ok());
    let minor = parts.next().and_then(|part| part.parse::<u32>().ok());
    match (major, minor) {
        (Some(major), Some(minor)) => major > 0 || minor >= 8,
        _ => false,
    }
}

fn record_from_row(row: &PgRow) -> Result<StoredMemory, StoreError> {
    let id: Uuid = row.try_get("id").map_err(corrupt)?;
    let scope_text: String = row.try_get("scope").map_err(corrupt)?;
    let kind_text: String = row.try_get("kind").map_err(corrupt)?;
    let content: String = row.try_get("content").map_err(corrupt)?;
    let confidence: i16 = row.try_get("confidence").map_err(corrupt)?;
    let source_uri: Option<String> = row.try_get("source_uri").map_err(corrupt)?;
    let source_agent: Option<String> = row.try_get("source_agent").map_err(corrupt)?;
    let source_external: Option<String> = row.try_get("source_external").map_err(corrupt)?;
    let vector_text: String = row.try_get("embedding_text").map_err(corrupt)?;
    let source = if source_uri.is_none() && source_agent.is_none() && source_external.is_none() {
        None
    } else {
        Some(rolio_core::Source {
            uri: source_uri,
            agent: source_agent,
            external_reference: source_external,
        })
    };
    Ok(StoredMemory {
        memory: Memory {
            id: MemoryId::from_uuid(id),
            scope: scope_from_text(&scope_text)?,
            kind: kind_from_text(&kind_text)?,
            content,
            confidence: Confidence::try_from(f32::from(confidence) / 10.0)
                .map_err(|_| corrupt(()))?,
            source,
            created_at: row
                .try_get::<OffsetDateTime, _>("created_at")
                .map_err(corrupt)?,
            updated_at: row
                .try_get::<OffsetDateTime, _>("updated_at")
                .map_err(corrupt)?,
        },
        revision: Revision(row.try_get::<Uuid, _>("revision").map_err(corrupt)?),
        vector: vector_from_text(&vector_text)?,
    })
}

fn scope_to_text(scope: &Scope) -> String {
    match scope {
        Scope::Global => String::from("global"),
        Scope::Project(key) => format!("project:{}", key.as_str()),
    }
}

fn scope_from_text(text: &str) -> Result<Scope, StoreError> {
    if text == "global" {
        return Ok(Scope::Global);
    }
    let key = text.strip_prefix("project:").ok_or_else(|| corrupt(()))?;
    Ok(Scope::Project(
        rolio_core::ProjectKey::new(key).map_err(|_| corrupt(()))?,
    ))
}

fn kind_to_text(kind: MemoryKind) -> &'static str {
    match kind {
        MemoryKind::Note => "note",
        MemoryKind::Preference => "preference",
        MemoryKind::Convention => "convention",
        MemoryKind::Fact => "fact",
        MemoryKind::Solution => "solution",
        MemoryKind::Lesson => "lesson",
        MemoryKind::Plan => "plan",
    }
}

fn kind_from_text(text: &str) -> Result<MemoryKind, StoreError> {
    match text {
        "note" => Ok(MemoryKind::Note),
        "preference" => Ok(MemoryKind::Preference),
        "convention" => Ok(MemoryKind::Convention),
        "fact" => Ok(MemoryKind::Fact),
        "solution" => Ok(MemoryKind::Solution),
        "lesson" => Ok(MemoryKind::Lesson),
        "plan" => Ok(MemoryKind::Plan),
        _ => Err(corrupt(())),
    }
}

fn distance_to_text(profile: &EmbeddingProfile) -> String {
    match profile.distance {
        DistanceMetric::Cosine => String::from("cosine"),
    }
}

fn distance_from_text(text: &str) -> Result<DistanceMetric, StoreError> {
    match text {
        "cosine" => Ok(DistanceMetric::Cosine),
        _ => Err(corrupt(())),
    }
}

fn vector_to_text(vector: &Vector) -> String {
    let mut text = String::with_capacity(vector.as_slice().len() * 12 + 2);
    text.push('[');
    for (index, value) in vector.as_slice().iter().enumerate() {
        if index > 0 {
            text.push(',');
        }
        text.push_str(&value.to_string());
    }
    text.push(']');
    text
}

fn vector_from_text(text: &str) -> Result<Vector, StoreError> {
    let inner = text
        .trim()
        .strip_prefix('[')
        .and_then(|rest| rest.strip_suffix(']'))
        .ok_or_else(|| corrupt(()))?;
    let values = inner
        .split(',')
        .map(|piece| piece.trim().parse::<f32>().map_err(|_| corrupt(())))
        .collect::<Result<Vec<_>, _>>()?;
    Vector::new(values).map_err(|_| corrupt(()))
}

fn content_hash(content: &str) -> String {
    hex(&Sha256::digest(content.as_bytes()))
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn hex_decode(text: &str, output: &mut [u8; 32]) -> Option<()> {
    let bytes = text.as_bytes();
    if bytes.len() != 64 {
        return None;
    }
    for index in 0..32 {
        let high = (bytes[index * 2] as char).to_digit(16)?;
        let low = (bytes[index * 2 + 1] as char).to_digit(16)?;
        output[index] = (high * 16 + low) as u8;
    }
    Some(())
}

/// Map any unexpected row or adapter failure to unavailable; read paths never
/// fabricate data.
fn corrupt<T>(_: T) -> StoreError {
    StoreError::Unavailable
}

#[cfg(test)]
mod tests {
    use std::sync::LazyLock;

    use rolio_core::{MemoryKind, ProjectKey, Source};

    use super::*;
    use crate::store::contracts;

    const DIMENSIONS: usize = 1024;

    /// PostgreSQL tests share one database; they serialize and use disjoint
    /// scopes. Set ROLIO_TEST_DATABASE_URL to run them, for example against
    /// `docker run -e POSTGRES_HOST_AUTH_METHOD=trust -p 5432:5432 pgvector/pgvector:17`.
    static TEST_LOCK: LazyLock<tokio::sync::Mutex<()>> =
        LazyLock::new(|| tokio::sync::Mutex::new(()));

    fn database_url() -> Option<String> {
        std::env::var("ROLIO_TEST_DATABASE_URL")
            .ok()
            .filter(|value| !value.is_empty())
    }

    fn identity() -> StoreIdentity {
        contracts::identity_with(NonZeroUsize::new(DIMENSIONS).unwrap())
    }

    async fn truncate(store: &PgStore) {
        sqlx::query("TRUNCATE memories")
            .execute(&store.pool)
            .await
            .unwrap();
    }

    fn record(scope: Scope, content: &str) -> WriteRequest {
        WriteRequest {
            memory: Memory {
                id: MemoryId::new(),
                scope,
                kind: MemoryKind::Fact,
                content: content.into(),
                confidence: Confidence::default(),
                source: Some(Source {
                    uri: None,
                    agent: Some("pg-test".into()),
                    external_reference: None,
                }),
                created_at: OffsetDateTime::UNIX_EPOCH,
                updated_at: OffsetDateTime::UNIX_EPOCH,
            },
            vector: Vector::new({
                let mut values = vec![1.0_f32, 0.0];
                values.resize(DIMENSIONS, 0.0);
                values
            })
            .unwrap(),
            condition: WriteCondition::Absent,
        }
    }

    #[tokio::test]
    async fn common_storage_contract() {
        let Some(url) = database_url() else {
            eprintln!("skipped: ROLIO_TEST_DATABASE_URL is not set");
            return;
        };
        let _guard = TEST_LOCK.lock().await;
        let store = PgStore::connect(&url, identity()).await.unwrap();
        truncate(&store).await;
        contracts::run_with_dimensions(&store, DIMENSIONS).await;
    }

    #[tokio::test]
    async fn persists_across_reconnect() {
        let Some(url) = database_url() else {
            eprintln!("skipped: ROLIO_TEST_DATABASE_URL is not set");
            return;
        };
        let _guard = TEST_LOCK.lock().await;
        let scope = Scope::Project(ProjectKey::new("pg-persistence").unwrap());
        let store = PgStore::connect(&url, identity()).await.unwrap();
        let created = store.write(record(scope, "Durable content")).await.unwrap();
        drop(store);
        let reopened = PgStore::connect(&url, identity()).await.unwrap();
        let read = reopened.get(created.memory.id).await.unwrap().unwrap();
        assert_eq!(read, created);
    }

    #[tokio::test]
    async fn filtered_search_finds_low_hitrate_scopes() {
        let Some(url) = database_url() else {
            eprintln!("skipped: ROLIO_TEST_DATABASE_URL is not set");
            return;
        };
        let _guard = TEST_LOCK.lock().await;
        // Unique scopes per run: leftover rows from earlier runs must not match.
        let run = MemoryId::new().to_string();
        let noise = Scope::Project(ProjectKey::new(format!("pg-noise-{run}")).unwrap());
        let target = Scope::Project(ProjectKey::new(format!("pg-target-{run}")).unwrap());
        let store = PgStore::connect(&url, identity()).await.unwrap();
        for index in 0..40 {
            let mut request = record(noise.clone(), &format!("Noise {index}"));
            request.memory.id = MemoryId::new();
            store.write(request).await.unwrap();
        }
        let wanted = store
            .write(record(target.clone(), "The only target row"))
            .await
            .unwrap();
        let hits = store
            .search(SearchRequest {
                vector: Vector::new({
                    let mut values = vec![1.0_f32, 0.0];
                    values.resize(DIMENSIONS, 0.0);
                    values
                })
                .unwrap(),
                scopes: vec![target],
                limit: NonZeroUsize::new(1).unwrap(),
            })
            .await
            .unwrap();
        assert_eq!(hits.len(), 1, "scope filter must not under-return");
        assert_eq!(hits[0].record.memory.id, wanted.memory.id);
    }
}
