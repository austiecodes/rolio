//! Shared semantic assertions for every adapter. Call `run` with an empty store
//! configured by `identity`. Real adapters also need persistence and fault tests.

use rolio_core::{Confidence, MemoryKind, ProjectKey, Source};
use time::OffsetDateTime;

use super::*;

pub(crate) fn identity() -> StoreIdentity {
    identity_with(size(2))
}

pub(crate) fn identity_with(dimensions: NonZeroUsize) -> StoreIdentity {
    StoreIdentity {
        schema_version: 1,
        embedding_profile: EmbeddingProfile {
            model: "contract-fixture".into(),
            artifact_digest: [0; 32],
            dimensions,
            distance: DistanceMetric::Cosine,
            document_preprocessing: "identity".into(),
            query_preprocessing: "identity".into(),
            pooling: "none".into(),
            normalization: "none".into(),
        },
    }
}

fn size(value: usize) -> NonZeroUsize {
    NonZeroUsize::new(value).unwrap()
}

fn scope(key: &str) -> Scope {
    Scope::Project(ProjectKey::new(key).unwrap())
}

/// A test vector with its meaningful part in the first two dimensions.
fn values(x: f32, y: f32, dimensions: usize) -> Vec<f32> {
    let mut vector = vec![x, y];
    vector.resize(dimensions, 0.0);
    vector
}

fn create(scope: Scope, content: &str, vector: Vec<f32>) -> WriteRequest {
    WriteRequest {
        memory: Memory {
            id: MemoryId::new(),
            scope,
            kind: MemoryKind::Convention,
            content: content.into(),
            confidence: Confidence::try_from(0.8).unwrap(),
            source: Some(Source {
                uri: Some("https://example.test/reference".into()),
                agent: Some("contract-test".into()),
                external_reference: Some("reference-1".into()),
            }),
            created_at: OffsetDateTime::UNIX_EPOCH,
            updated_at: OffsetDateTime::UNIX_EPOCH,
        },
        vector: Vector::new(vector).unwrap(),
        condition: WriteCondition::Absent,
    }
}

fn replace(record: &StoredMemory, content: &str, vector: Vec<f32>) -> WriteRequest {
    let mut memory = record.memory.clone();
    memory.content = content.into();
    memory.updated_at += time::Duration::seconds(1);
    WriteRequest {
        memory,
        vector: Vector::new(vector).unwrap(),
        condition: WriteCondition::Revision(record.revision),
    }
}

async fn search(store: &dyn MemoryStore, scopes: Vec<Scope>, dimensions: usize) -> Vec<SearchHit> {
    store
        .search(SearchRequest {
            vector: Vector::new(values(1.0, 0.0, dimensions)).unwrap(),
            scopes,
            limit: size(10),
        })
        .await
        .unwrap()
}

pub(crate) async fn run(store: &dyn MemoryStore) {
    run_with_dimensions(store, 2).await;
}

/// Run the shared contract at a fixed vector width. Real adapters verify the
/// width they are actually deployed with.
pub(crate) async fn run_with_dimensions(store: &dyn MemoryStore, dimensions: usize) {
    identity_checks(store, dimensions).await;
    lifecycle(store, dimensions).await;
    concurrent_conditions(store, dimensions).await;
    invalid_writes_leave_record_unchanged(store, dimensions).await;
    scoped_pagination(store, dimensions).await;
    scoped_search(store, dimensions).await;
}

async fn identity_checks(store: &dyn MemoryStore, dimensions: usize) {
    let identity = identity_with(size(dimensions));
    store.check(&identity).await.unwrap();
    let mut wrong = identity.clone();
    wrong.schema_version += 1;
    assert_eq!(
        store.check(&wrong).await,
        Err(StoreError::IncompatibleIdentity)
    );
    wrong = identity.clone();
    wrong.embedding_profile.artifact_digest[0] = 1;
    assert_eq!(
        store.check(&wrong).await,
        Err(StoreError::IncompatibleIdentity)
    );
    wrong = identity;
    wrong.embedding_profile.query_preprocessing = "different-prefix".into();
    assert_eq!(
        store.check(&wrong).await,
        Err(StoreError::IncompatibleIdentity)
    );
}

async fn lifecycle(store: &dyn MemoryStore, dimensions: usize) {
    let selected = scope("contract-lifecycle");
    let request = create(
        selected.clone(),
        "Original content",
        values(1.0, 0.0, dimensions),
    );
    let id = request.memory.id;
    assert!(store.get(id).await.unwrap().is_none());
    let original = store.write(request.clone()).await.unwrap();
    assert_eq!(original.memory, request.memory);
    assert_eq!(original.vector, request.vector);
    assert_eq!(store.get(id).await.unwrap(), Some(original.clone()));
    assert_eq!(store.write(request).await, Err(StoreError::AlreadyExists));
    assert_eq!(store.get(id).await.unwrap(), Some(original.clone()));

    let replacement = replace(&original, "Updated content", values(0.0, 1.0, dimensions));
    let updated = store.write(replacement.clone()).await.unwrap();
    assert_ne!(updated.revision, original.revision);
    assert_eq!(updated.memory, replacement.memory);
    assert_eq!(updated.vector, replacement.vector);
    assert_eq!(store.get(id).await.unwrap(), Some(updated.clone()));
    let hits = search(store, vec![selected.clone()], dimensions).await;
    assert_eq!(hits.len(), 1);
    assert_eq!(hits[0].record, updated);

    assert_eq!(
        store.write(replacement).await,
        Err(StoreError::RevisionConflict)
    );
    assert_eq!(
        store.delete(id, original.revision).await,
        Err(StoreError::RevisionConflict)
    );
    assert_eq!(store.get(id).await.unwrap(), Some(updated.clone()));
    store.delete(id, updated.revision).await.unwrap();
    assert!(store.get(id).await.unwrap().is_none());
    assert!(search(store, vec![selected], dimensions).await.is_empty());
    assert_eq!(
        store.delete(id, updated.revision).await,
        Err(StoreError::RevisionConflict)
    );
    assert_eq!(
        store
            .write(replace(
                &updated,
                "Do not recreate",
                values(1.0, 0.0, dimensions)
            ))
            .await,
        Err(StoreError::RevisionConflict)
    );
}

async fn concurrent_conditions(store: &dyn MemoryStore, dimensions: usize) {
    let request = create(
        scope("contract-concurrent"),
        "Initial content",
        values(1.0, 0.0, dimensions),
    );
    let (left, right) = tokio::join!(store.write(request.clone()), store.write(request));
    let created = match (left, right) {
        (Ok(record), Err(StoreError::AlreadyExists))
        | (Err(StoreError::AlreadyExists), Ok(record)) => record,
        results => panic!("Exactly one create must succeed: {results:?}"),
    };
    let first = replace(&created, "First candidate", values(0.0, 1.0, dimensions));
    let second = replace(&created, "Second candidate", values(-1.0, 0.0, dimensions));
    let (left, right) = tokio::join!(store.write(first.clone()), store.write(second.clone()));
    let (winner, expected) = match (left, right) {
        (Ok(record), Err(StoreError::RevisionConflict)) => (record, first),
        (Err(StoreError::RevisionConflict), Ok(record)) => (record, second),
        results => panic!("Exactly one conditional update must succeed: {results:?}"),
    };
    assert_eq!(winner.memory, expected.memory);
    assert_eq!(winner.vector, expected.vector);
    assert_ne!(winner.revision, created.revision);
    assert_eq!(store.get(created.memory.id).await.unwrap(), Some(winner));
}

async fn invalid_writes_leave_record_unchanged(store: &dyn MemoryStore, dimensions: usize) {
    let original = store
        .write(create(
            scope("contract-validation"),
            "Valid",
            values(1.0, 0.0, dimensions),
        ))
        .await
        .unwrap();
    let mut request = replace(&original, "Different scope", values(0.0, 1.0, dimensions));
    request.memory.scope = Scope::Global;
    assert_eq!(store.write(request).await, Err(StoreError::ScopeChange));
    let request = replace(
        &original,
        "Wrong dimension",
        values(1.0, 0.0, dimensions - 1),
    );
    assert_eq!(
        store.write(request).await,
        Err(StoreError::DimensionMismatch)
    );
    let request = replace(&original, " \n\t", values(1.0, 0.0, dimensions));
    assert_eq!(store.write(request).await, Err(StoreError::InvalidMemory));
    assert_eq!(store.get(original.memory.id).await.unwrap(), Some(original));
}

async fn scoped_pagination(store: &dyn MemoryStore, dimensions: usize) {
    let selected = scope("contract-pages");
    let mut expected = Vec::new();
    for index in 0..5 {
        let record = store
            .write(create(
                selected.clone(),
                &format!("Page item {index}"),
                values(1.0, 0.0, dimensions),
            ))
            .await
            .unwrap();
        expected.push(record.memory.id);
    }
    store
        .write(create(
            Scope::Global,
            "Not in project pages",
            values(1.0, 0.0, dimensions),
        ))
        .await
        .unwrap();
    expected.sort();
    let mut cursor = None;
    let mut actual = Vec::new();
    for page_index in 0..3 {
        let page = store
            .list(ListRequest {
                scope: selected.clone(),
                cursor,
                limit: size(2),
            })
            .await
            .unwrap();
        assert!(
            page.records
                .iter()
                .all(|record| record.memory.scope == selected)
        );
        assert_eq!(page.records.len(), if page_index == 2 { 1 } else { 2 });
        actual.extend(page.records.iter().map(|record| record.memory.id));
        if let Some(next) = &page.next {
            assert_eq!(next.scope, selected);
            assert_eq!(next.after, page.records.last().unwrap().memory.id);
        }
        cursor = page.next;
        assert_eq!(cursor.is_none(), page_index == 2);
    }
    assert_eq!(actual, expected);
    let invalid = store
        .list(ListRequest {
            scope: Scope::Global,
            cursor: Some(ListCursor {
                scope: selected.clone(),
                after: expected[0],
            }),
            limit: size(2),
        })
        .await;
    assert!(matches!(invalid, Err(StoreError::CursorMismatch)));
    // A keyset cursor remains usable when its boundary record is deleted.
    let boundary = store.get(expected[1]).await.unwrap().unwrap();
    store
        .delete(boundary.memory.id, boundary.revision)
        .await
        .unwrap();
    let page = store
        .list(ListRequest {
            scope: selected.clone(),
            cursor: Some(ListCursor {
                scope: selected,
                after: boundary.memory.id,
            }),
            limit: size(10),
        })
        .await
        .unwrap();
    assert_eq!(
        page.records
            .iter()
            .map(|record| record.memory.id)
            .collect::<Vec<_>>(),
        expected[2..]
    );
    assert!(page.next.is_none());
    let empty = store
        .list(ListRequest {
            scope: scope("contract-empty"),
            cursor: None,
            limit: size(10),
        })
        .await
        .unwrap();
    assert!(empty.records.is_empty());
    assert!(empty.next.is_none());
}

async fn scoped_search(store: &dyn MemoryStore, dimensions: usize) {
    let selected = scope("contract/'scope:\u{6d4b}\u{8bd5}");
    let other = scope("contract-other");
    let wanted = store
        .write(create(
            selected.clone(),
            "Selected scope",
            values(0.0, 1.0, dimensions),
        ))
        .await
        .unwrap();
    let excluded = store
        .write(create(
            other.clone(),
            "Closer but excluded",
            values(1.0, 0.0, dimensions),
        ))
        .await
        .unwrap();
    let hits = store
        .search(SearchRequest {
            vector: Vector::new(values(1.0, 0.0, dimensions)).unwrap(),
            scopes: vec![selected.clone()],
            limit: size(1),
        })
        .await
        .unwrap();
    assert_eq!(
        hits.len(),
        1,
        "Filtering must not discard the selected scope after top-k"
    );
    assert_eq!(hits[0].record, wanted);
    assert!(hits[0].score.is_finite());
    let hits = search(store, vec![selected, other.clone()], dimensions).await;
    assert_eq!(hits.len(), 2);
    assert!(
        hits.iter()
            .any(|hit| hit.record.memory.id == excluded.memory.id)
    );
    let invalid = store
        .search(SearchRequest {
            vector: Vector::new(values(1.0, 0.0, dimensions)).unwrap(),
            scopes: vec![],
            limit: size(1),
        })
        .await;
    assert!(matches!(invalid, Err(StoreError::MissingScope)));
    let invalid = store
        .search(SearchRequest {
            vector: Vector::new(values(1.0, 0.0, dimensions - 1)).unwrap(),
            scopes: vec![other],
            limit: size(1),
        })
        .await;
    assert!(matches!(invalid, Err(StoreError::DimensionMismatch)));
}

#[test]
fn vectors_reject_invalid_cosine_inputs() {
    for values in [
        vec![],
        vec![0.0, -0.0],
        vec![f32::NAN],
        vec![f32::INFINITY],
        vec![f32::NEG_INFINITY],
    ] {
        assert_eq!(Vector::new(values), Err(StoreError::InvalidVector));
    }
    assert!(Vector::new(vec![f32::MAX, f32::MIN]).is_ok());
    assert!(Vector::new(vec![f32::MIN_POSITIVE]).is_ok());
}

#[test]
fn revision_round_trip() {
    let revision = Revision::new();
    assert_eq!(revision.to_string().parse::<Revision>().unwrap(), revision);
    assert!("invalid-revision".parse::<Revision>().is_err());
    assert_ne!(revision, Revision::new());
}
