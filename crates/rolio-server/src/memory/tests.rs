use std::{collections::VecDeque, future::Future, sync::Mutex, time::Duration};

use async_trait::async_trait;
use rolio_core::ProjectKey;
use tokio::sync::Notify;

use super::*;
use crate::{
    embedding::Embedding,
    store::{EmbeddingProfile, contracts, fake::FakeStore},
};

const TEST_TIMEOUT: Duration = Duration::from_secs(5);

async fn bounded<T>(future: impl Future<Output = T>) -> T {
    tokio::time::timeout(TEST_TIMEOUT, future)
        .await
        .expect("Test operation exceeded its deadline")
}

#[derive(Default)]
struct EmbeddingGate {
    started: Notify,
    proceed: Notify,
}

impl EmbeddingGate {
    async fn wait_started(&self) {
        bounded(self.started.notified()).await;
    }

    fn release(&self) {
        self.proceed.notify_one();
    }
}

struct EmbeddingStep {
    result: Result<Embedding, EmbeddingError>,
    gate: Option<Arc<EmbeddingGate>>,
}

#[derive(Default)]
struct EmbeddingState {
    calls: Vec<(String, InputKind)>,
    steps: VecDeque<EmbeddingStep>,
}

/// A scripted provider for service tests. No model or worker is involved.
struct TestEmbedding {
    profile: EmbeddingProfile,
    state: Mutex<EmbeddingState>,
}

impl TestEmbedding {
    fn new(profile: EmbeddingProfile) -> Self {
        Self {
            profile,
            state: Mutex::new(EmbeddingState::default()),
        }
    }

    fn result(&self, values: impl Into<Vec<f32>>) -> Embedding {
        Embedding {
            profile: self.profile.clone(),
            vector: vector(values),
        }
    }

    fn respond_next(&self, result: Result<Embedding, EmbeddingError>) {
        self.state
            .lock()
            .unwrap()
            .steps
            .push_back(EmbeddingStep { result, gate: None });
    }

    fn pause_next(&self, values: [f32; 2]) -> Arc<EmbeddingGate> {
        let gate = Arc::new(EmbeddingGate::default());
        self.state.lock().unwrap().steps.push_back(EmbeddingStep {
            result: Ok(self.result(values)),
            gate: Some(gate.clone()),
        });
        gate
    }

    fn calls(&self) -> Vec<(String, InputKind)> {
        self.state.lock().unwrap().calls.clone()
    }
}

#[async_trait]
impl EmbeddingProvider for TestEmbedding {
    fn profile(&self) -> &EmbeddingProfile {
        &self.profile
    }

    async fn embed(&self, text: &str, kind: InputKind) -> Result<Embedding, EmbeddingError> {
        let step = {
            let mut state = self.state.lock().unwrap();
            state.calls.push((text.into(), kind));
            state.steps.pop_front()
        };
        let step = step.unwrap_or_else(|| EmbeddingStep {
            result: Ok(self.result([1.0, 0.0])),
            gate: None,
        });
        if let Some(gate) = step.gate {
            gate.started.notify_one();
            bounded(gate.proceed.notified()).await;
        }
        step.result
    }
}

async fn setup() -> (MemoryService, Arc<FakeStore>, Arc<TestEmbedding>) {
    let identity = contracts::identity();
    let store = Arc::new(FakeStore::new(identity.clone()));
    let embedding = Arc::new(TestEmbedding::new(identity.embedding_profile.clone()));
    let service = MemoryService::new(store.clone(), embedding.clone(), identity)
        .await
        .unwrap();
    (service, store, embedding)
}

fn size(value: usize) -> NonZeroUsize {
    NonZeroUsize::new(value).unwrap()
}

fn id(value: u32) -> MemoryId {
    format!("00000000-0000-0000-0000-{value:012x}")
        .parse()
        .unwrap()
}

fn project(key: &str) -> Scope {
    Scope::Project(ProjectKey::new(key).unwrap())
}

fn vector(values: impl Into<Vec<f32>>) -> Vector {
    Vector::new(values.into()).unwrap()
}

fn input(content: impl Into<String>) -> MemoryInput {
    MemoryInput {
        content: content.into(),
        kind: MemoryKind::Note,
        confidence: Confidence::default(),
        source: None,
    }
}

fn source() -> Source {
    Source {
        uri: Some(" HTTPS://Example.TEST/a/../b?x=1 ".into()),
        agent: Some(" test-agent ".into()),
        external_reference: Some("entry\n42".into()),
    }
}

#[tokio::test]
async fn initialization_requires_matching_provider_and_store_identity() {
    let identity = contracts::identity();
    let store = Arc::new(FakeStore::new(identity.clone()));
    let mut other = identity.clone();
    other.embedding_profile.artifact_digest[0] = 1;
    let embedding = Arc::new(TestEmbedding::new(other.embedding_profile.clone()));
    assert_eq!(
        MemoryService::new(store.clone(), embedding.clone(), identity.clone())
            .await
            .err(),
        Some(MemoryError::Embedding(EmbeddingError::IncompatibleProfile))
    );
    assert!(embedding.calls().is_empty());

    for expected in [
        other,
        StoreIdentity {
            schema_version: identity.schema_version + 1,
            ..identity.clone()
        },
    ] {
        let embedding = Arc::new(TestEmbedding::new(expected.embedding_profile.clone()));
        assert_eq!(
            MemoryService::new(store.clone(), embedding.clone(), expected)
                .await
                .err(),
            Some(MemoryError::Store(StoreError::IncompatibleIdentity))
        );
        assert!(embedding.calls().is_empty());
    }

    let embedding = Arc::new(TestEmbedding::new(identity.embedding_profile.clone()));
    let service = MemoryService::new(store, embedding.clone(), identity)
        .await
        .unwrap();
    service.check().await.unwrap();
    assert!(embedding.calls().is_empty());
}

#[tokio::test]
async fn invalid_content_and_source_are_rejected_before_embedding() {
    let (service, store, embedding) = setup().await;
    let original = service
        .remember(id(1), Scope::Global, input("Original"))
        .await
        .unwrap();
    let mut cases = vec![
        ("empty content", input("")),
        ("whitespace content", input(" \t\n\u{2003}")),
        (
            "content byte limit",
            input("a".repeat(MemoryInput::MAX_CONTENT_BYTES + 1)),
        ),
        (
            "UTF-8 content byte limit",
            input("\u{e9}".repeat(MemoryInput::MAX_CONTENT_BYTES / 2 + 1)),
        ),
    ];
    for (name, source) in [
        (
            "URI byte limit",
            Source {
                uri: Some(format!("{}a", "\u{e9}".repeat(1024))),
                ..Source::default()
            },
        ),
        (
            "agent byte limit",
            Source {
                agent: Some("a".repeat(257)),
                ..Source::default()
            },
        ),
        (
            "reference byte limit",
            Source {
                external_reference: Some("a".repeat(2049)),
                ..Source::default()
            },
        ),
        (
            "combined source byte limit",
            Source {
                uri: Some("a".repeat(2048)),
                agent: Some("a".into()),
                external_reference: Some("a".repeat(2048)),
            },
        ),
    ] {
        cases.push((
            name,
            MemoryInput {
                source: Some(source),
                ..input("Changed body")
            },
        ));
    }
    for (name, candidate) in cases {
        assert_eq!(
            service
                .remember(id(2), Scope::Global, candidate.clone())
                .await,
            Err(MemoryError::InvalidInput),
            "remember: {name}"
        );
        assert_eq!(
            service.update(id(1), original.revision, candidate).await,
            Err(MemoryError::InvalidInput),
            "update: {name}"
        );
        assert_eq!(store.get(id(2)).await.unwrap(), None);
        assert_eq!(service.get(id(1)).await.unwrap(), original);
    }
    assert_eq!(
        embedding.calls(),
        [("Original".into(), InputKind::Document)]
    );
}

#[tokio::test]
async fn exact_content_and_source_byte_limits_are_accepted() {
    let (service, _, embedding) = setup().await;
    for source in [
        Source {
            uri: Some("u".repeat(2048)),
            agent: Some("a".repeat(256)),
            external_reference: Some("r".repeat(1792)),
        },
        Source {
            uri: Some("u".repeat(2048)),
            agent: None,
            external_reference: Some("r".repeat(2048)),
        },
    ] {
        let candidate = MemoryInput {
            source: Some(source),
            ..input("\u{e9}".repeat(MemoryInput::MAX_CONTENT_BYTES / 2))
        };
        let record = service
            .remember(MemoryId::new(), Scope::Global, candidate.clone())
            .await
            .unwrap();
        assert_eq!(record.memory.content, candidate.content);
        assert_eq!(record.memory.source, candidate.source);
    }
    assert_eq!(embedding.calls().len(), 2);
}

#[tokio::test]
async fn embedding_errors_do_not_create_or_replace_memories() {
    let (service, store, embedding) = setup().await;
    let original = service
        .remember(id(1), Scope::Global, input("Original"))
        .await
        .unwrap();

    embedding.respond_next(Err(EmbeddingError::Unavailable));
    assert_eq!(
        service
            .remember(id(2), Scope::Global, input("Failed create"))
            .await,
        Err(MemoryError::Embedding(EmbeddingError::Unavailable))
    );
    embedding.respond_next(Err(EmbeddingError::Timeout));
    assert_eq!(
        service
            .update(id(1), original.revision, input("Failed update"))
            .await,
        Err(MemoryError::Embedding(EmbeddingError::Timeout))
    );
    embedding.respond_next(Err(EmbeddingError::TokenLimit));
    assert_eq!(
        service
            .recall("Query", vec![Scope::Global], size(10))
            .await
            .err(),
        Some(MemoryError::Embedding(EmbeddingError::TokenLimit))
    );
    assert_eq!(store.get(id(2)).await.unwrap(), None);
    assert_eq!(service.get(id(1)).await.unwrap(), original);
    assert_eq!(
        embedding.calls(),
        [
            ("Original".into(), InputKind::Document),
            ("Failed create".into(), InputKind::Document),
            ("Failed update".into(), InputKind::Document),
            ("Query".into(), InputKind::Query),
        ]
    );
}

#[tokio::test]
async fn result_profile_and_dimension_mismatches_are_rejected() {
    for wrong_profile in [true, false] {
        let (service, store, embedding) = setup().await;
        let original = service
            .remember(id(1), Scope::Global, input("Original"))
            .await
            .unwrap();
        let mut result = embedding.result([0.0, 1.0]);
        if wrong_profile {
            result.profile.query_preprocessing = "unexpected-prefix".into();
        } else {
            result.vector = vector([1.0]);
        }
        let expected = || {
            if wrong_profile {
                MemoryError::Embedding(EmbeddingError::IncompatibleProfile)
            } else {
                MemoryError::Store(StoreError::DimensionMismatch)
            }
        };
        embedding.respond_next(Ok(result.clone()));
        assert_eq!(
            service
                .remember(id(2), Scope::Global, input("New memory"))
                .await,
            Err(expected())
        );
        embedding.respond_next(Ok(result.clone()));
        assert_eq!(
            service
                .update(id(1), original.revision, input("Changed body"))
                .await,
            Err(expected())
        );
        embedding.respond_next(Ok(result));
        assert_eq!(
            service
                .recall("Query", vec![Scope::Global], size(10))
                .await
                .err(),
            Some(expected())
        );
        assert_eq!(store.get(id(2)).await.unwrap(), None);
        assert_eq!(service.get(id(1)).await.unwrap(), original);
    }
}

#[tokio::test]
async fn remember_sets_server_timestamps_and_preserves_input_text() {
    let (service, _, embedding) = setup().await;
    let candidate = MemoryInput {
        source: Some(source()),
        ..input("\n\tKeep exact text.\r\n")
    };
    let before = Timestamp::now_utc();
    let record = service
        .remember(id(1), project("timestamps"), candidate.clone())
        .await
        .unwrap();
    let after = Timestamp::now_utc();
    assert_eq!(record.memory.id, id(1));
    assert_eq!(record.memory.scope, project("timestamps"));
    assert_eq!(record.memory.content, candidate.content);
    assert_eq!(record.memory.source, candidate.source);
    assert_eq!(record.memory.created_at, record.memory.updated_at);
    assert!((before..=after).contains(&record.memory.created_at));
    assert_eq!(record.memory.created_at.offset(), time::UtcOffset::UTC);
    assert_eq!(
        embedding.calls(),
        [(candidate.content, InputKind::Document)]
    );
}

#[tokio::test]
async fn body_changes_reembed_but_metadata_replacements_reuse_the_vector() {
    let (service, _, embedding) = setup().await;
    let original = service
        .remember(
            id(1),
            project("replacement"),
            MemoryInput {
                source: Some(source()),
                ..input("Original")
            },
        )
        .await
        .unwrap();
    let changed = MemoryInput {
        content: "Changed body".into(),
        kind: MemoryKind::Fact,
        confidence: Confidence::try_from(0.4).unwrap(),
        source: Some(Source {
            agent: Some("replacement-agent".into()),
            ..Source::default()
        }),
    };
    embedding.respond_next(Ok(embedding.result([0.0, 1.0])));
    let reembedded = service
        .update(id(1), original.revision, changed.clone())
        .await
        .unwrap();
    assert_eq!(reembedded.memory.content, changed.content);
    assert_eq!(reembedded.memory.kind, changed.kind);
    assert_eq!(reembedded.memory.confidence, changed.confidence);
    assert_eq!(reembedded.memory.source, changed.source);
    assert_eq!(reembedded.vector, vector([0.0, 1.0]));
    assert_ne!(reembedded.revision, original.revision);

    // This error must remain unused: a metadata-only update needs no inference.
    embedding.respond_next(Err(EmbeddingError::Unavailable));
    let metadata = MemoryInput {
        content: changed.content,
        kind: MemoryKind::Lesson,
        confidence: Confidence::try_from(0.0).unwrap(),
        source: None,
    };
    let replaced = service
        .update(id(1), reembedded.revision, metadata.clone())
        .await
        .unwrap();
    assert_eq!(replaced.memory.content, metadata.content);
    assert_eq!(replaced.memory.kind, metadata.kind);
    assert_eq!(replaced.memory.confidence, metadata.confidence);
    assert_eq!(replaced.memory.source, None);
    assert_eq!(replaced.vector, reembedded.vector);
    assert_ne!(replaced.revision, reembedded.revision);
    for record in [&reembedded, &replaced] {
        assert_eq!(record.memory.id, original.memory.id);
        assert_eq!(record.memory.scope, original.memory.scope);
        assert_eq!(record.memory.created_at, original.memory.created_at);
        assert!(record.memory.updated_at >= original.memory.updated_at);
    }
    assert!(replaced.memory.updated_at >= reembedded.memory.updated_at);
    assert_eq!(service.get(id(1)).await.unwrap(), replaced);
    assert_eq!(
        embedding.calls(),
        [
            ("Original".into(), InputKind::Document),
            ("Changed body".into(), InputKind::Document),
        ]
    );
}

#[tokio::test]
async fn updates_preserve_creation_time_and_never_move_time_backwards() {
    let (service, store, embedding) = setup().await;
    for previous_time in [
        Timestamp::UNIX_EPOCH,
        Timestamp::now_utc() + time::Duration::days(1),
    ] {
        let original = store
            .write(WriteRequest {
                memory: Memory {
                    id: MemoryId::new(),
                    scope: project("clock"),
                    content: "Unchanged body".into(),
                    kind: MemoryKind::Note,
                    confidence: Confidence::default(),
                    source: None,
                    created_at: Timestamp::UNIX_EPOCH,
                    updated_at: previous_time,
                },
                vector: vector([0.0, 1.0]),
                condition: WriteCondition::Absent,
            })
            .await
            .unwrap();
        let before = Timestamp::now_utc();
        let updated = service
            .update(
                original.memory.id,
                original.revision,
                input("Unchanged body"),
            )
            .await
            .unwrap();
        let after = Timestamp::now_utc();
        assert_eq!(updated.memory.created_at, Timestamp::UNIX_EPOCH);
        assert!(
            (before.max(previous_time)..=after.max(previous_time))
                .contains(&updated.memory.updated_at)
        );
        assert_eq!(updated.vector, original.vector);
    }
    assert!(embedding.calls().is_empty());
}

#[tokio::test]
async fn missing_records_and_stale_revisions_reject_updates_before_embedding() {
    let (service, _, embedding) = setup().await;
    let original = service
        .remember(id(1), Scope::Global, input("Original"))
        .await
        .unwrap();
    assert_eq!(
        service
            .update(id(2), Revision::new(), input("Missing"))
            .await,
        Err(MemoryError::NotFound)
    );
    assert_eq!(
        service.update(id(1), Revision::new(), input("Stale")).await,
        Err(MemoryError::Store(StoreError::RevisionConflict))
    );
    assert_eq!(service.get(id(1)).await.unwrap(), original);
    assert_eq!(
        embedding.calls(),
        [("Original".into(), InputKind::Document)]
    );
}

#[tokio::test]
async fn final_update_cas_rejects_a_write_that_becomes_stale_during_embedding() {
    let (service, _, embedding) = setup().await;
    let original = service
        .remember(id(1), Scope::Global, input("Original"))
        .await
        .unwrap();
    let gate = embedding.pause_next([0.0, 1.0]);
    let (stale, winner) = bounded(async {
        tokio::join!(
            service.update(id(1), original.revision, input("Slow candidate")),
            async {
                gate.wait_started().await;
                assert_eq!(service.get(id(1)).await.unwrap(), original);
                let winner = service
                    .update(
                        id(1),
                        original.revision,
                        MemoryInput {
                            kind: MemoryKind::Fact,
                            ..input("Original")
                        },
                    )
                    .await
                    .unwrap();
                gate.release();
                winner
            }
        )
    })
    .await;
    assert_eq!(stale, Err(MemoryError::Store(StoreError::RevisionConflict)));
    assert_ne!(winner.revision, original.revision);
    assert_eq!(winner.vector, original.vector);
    assert_eq!(service.get(id(1)).await.unwrap(), winner);
    assert_eq!(
        embedding.calls(),
        [
            ("Original".into(), InputKind::Document),
            ("Slow candidate".into(), InputKind::Document),
        ]
    );
}

#[tokio::test]
async fn deletion_during_embedding_rejects_stale_updates_even_after_recreation() {
    for recreate in [false, true] {
        let (service, store, embedding) = setup().await;
        let original = service
            .remember(id(1), Scope::Global, input("Original"))
            .await
            .unwrap();
        let gate = embedding.pause_next([0.0, 1.0]);
        let (stale, replacement) = bounded(async {
            tokio::join!(
                service.update(id(1), original.revision, input("Slow candidate")),
                async {
                    gate.wait_started().await;
                    service.forget(id(1), original.revision).await.unwrap();
                    assert_eq!(service.get(id(1)).await, Err(MemoryError::NotFound));
                    let replacement = if recreate {
                        Some(
                            service
                                .remember(id(1), Scope::Global, input("Recreated"))
                                .await
                                .unwrap(),
                        )
                    } else {
                        None
                    };
                    gate.release();
                    replacement
                }
            )
        })
        .await;
        assert_eq!(stale, Err(MemoryError::Store(StoreError::RevisionConflict)));
        assert_eq!(store.get(id(1)).await.unwrap(), replacement);
        if let Some(replacement) = replacement {
            assert_ne!(replacement.revision, original.revision);
            assert_eq!(replacement.memory.content, "Recreated");
            assert_eq!(replacement.vector, vector([1.0, 0.0]));
        }
    }
}

#[tokio::test]
async fn concurrent_remember_uses_the_final_absent_condition() {
    let (service, store, embedding) = setup().await;
    let gate = embedding.pause_next([0.0, 1.0]);
    let (stale, winner) = bounded(async {
        tokio::join!(
            service.remember(id(1), Scope::Global, input("Slow create")),
            async {
                gate.wait_started().await;
                assert_eq!(store.get(id(1)).await.unwrap(), None);
                let winner = service
                    .remember(id(1), Scope::Global, input("Winning create"))
                    .await
                    .unwrap();
                gate.release();
                winner
            }
        )
    })
    .await;
    assert_eq!(stale, Err(MemoryError::Store(StoreError::AlreadyExists)));
    assert_eq!(service.get(id(1)).await.unwrap(), winner);
    assert_eq!(winner.memory.content, "Winning create");
    assert_eq!(winner.vector, vector([1.0, 0.0]));
    assert_eq!(
        embedding.calls(),
        [
            ("Slow create".into(), InputKind::Document),
            ("Winning create".into(), InputKind::Document),
        ]
    );
}

#[tokio::test]
async fn get_list_and_forget_preserve_store_results_and_scope_boundaries() {
    let (service, _, embedding) = setup().await;
    let selected = project("selected");
    let later = service
        .remember(id(3), selected.clone(), input("Later ID"))
        .await
        .unwrap();
    let first = service
        .remember(id(1), selected.clone(), input("First ID"))
        .await
        .unwrap();
    let global = service
        .remember(id(2), Scope::Global, input("Global"))
        .await
        .unwrap();
    let calls = embedding.calls();
    assert_eq!(service.get(id(1)).await.unwrap(), first);
    assert_eq!(service.get(id(4)).await, Err(MemoryError::NotFound));
    let page = service
        .list(ListRequest {
            scope: selected.clone(),
            cursor: None,
            limit: size(1),
        })
        .await
        .unwrap();
    assert_eq!(page.records, std::slice::from_ref(&first));
    assert!(page.next.is_some());
    let next = service
        .list(ListRequest {
            scope: selected,
            cursor: page.next,
            limit: size(1),
        })
        .await
        .unwrap();
    assert_eq!(next.records, [later]);
    assert!(next.next.is_none());
    assert_eq!(
        service
            .list(ListRequest {
                scope: Scope::Global,
                cursor: None,
                limit: size(10),
            })
            .await
            .unwrap()
            .records,
        [global]
    );
    assert_eq!(
        service.forget(id(1), Revision::new()).await,
        Err(MemoryError::Store(StoreError::RevisionConflict))
    );
    assert_eq!(service.get(id(1)).await.unwrap(), first);
    service.forget(id(1), first.revision).await.unwrap();
    assert_eq!(service.get(id(1)).await, Err(MemoryError::NotFound));
    assert_eq!(
        service.forget(id(1), first.revision).await,
        Err(MemoryError::Store(StoreError::RevisionConflict))
    );
    assert_eq!(embedding.calls(), calls);
}

#[tokio::test]
async fn recall_and_list_limits_reject_invalid_requests_before_embedding() {
    let (service, _, embedding) = setup().await;
    let mut cases = vec![
        (String::new(), vec![Scope::Global], size(1)),
        (" \n\t\u{2003}".into(), vec![Scope::Global], size(1)),
        (
            "a".repeat(MemoryService::MAX_QUERY_BYTES + 1),
            vec![Scope::Global],
            size(1),
        ),
        (
            "\u{e9}".repeat(MemoryService::MAX_QUERY_BYTES / 2 + 1),
            vec![Scope::Global],
            size(1),
        ),
        ("Query".into(), vec![], size(1)),
        (
            "Query".into(),
            vec![Scope::Global],
            size(MemoryService::MAX_RESULTS + 1),
        ),
    ];
    let scopes: Vec<_> = (0..=MemoryService::MAX_SCOPES)
        .map(|index| project(&format!("project-{index}")))
        .collect();
    cases.push(("Query".into(), scopes, size(1)));
    for (query, scopes, limit) in cases {
        assert_eq!(
            service.recall(&query, scopes, limit).await.err(),
            Some(MemoryError::InvalidInput)
        );
    }
    assert_eq!(
        service
            .list(ListRequest {
                scope: Scope::Global,
                cursor: None,
                limit: size(MemoryService::MAX_RESULTS + 1),
            })
            .await
            .err(),
        Some(MemoryError::InvalidInput)
    );
    assert!(embedding.calls().is_empty());

    let query = "\u{e9}".repeat(MemoryService::MAX_QUERY_BYTES / 2);
    let scopes = (0..MemoryService::MAX_SCOPES)
        .map(|index| project(&format!("project-{index}")))
        .collect();
    assert!(
        service
            .recall(&query, scopes, size(MemoryService::MAX_RESULTS))
            .await
            .unwrap()
            .is_empty()
    );
    assert_eq!(embedding.calls(), [(query, InputKind::Query)]);
}

#[tokio::test]
async fn recall_embeds_queries_and_searches_only_explicit_scopes() {
    let (service, _, embedding) = setup().await;
    let selected = project("selected");
    embedding.respond_next(Ok(embedding.result([0.0, 1.0])));
    let wanted = service
        .remember(id(1), selected.clone(), input("Selected"))
        .await
        .unwrap();
    let global = service
        .remember(id(2), Scope::Global, input("Global"))
        .await
        .unwrap();
    service
        .remember(id(3), project("excluded"), input("Closer but excluded"))
        .await
        .unwrap();
    let hits = service
        .recall("Query", vec![selected.clone()], size(1))
        .await
        .unwrap();
    assert_eq!(hits.len(), 1);
    assert_eq!(hits[0].record, wanted);
    let hits = service
        .recall(
            "Query",
            vec![selected.clone(), Scope::Global, selected],
            size(10),
        )
        .await
        .unwrap();
    assert_eq!(
        hits.into_iter().map(|hit| hit.record).collect::<Vec<_>>(),
        [global, wanted]
    );
    assert_eq!(
        embedding.calls(),
        [
            ("Selected".into(), InputKind::Document),
            ("Global".into(), InputKind::Document),
            ("Closer but excluded".into(), InputKind::Document),
            ("Query".into(), InputKind::Query),
            ("Query".into(), InputKind::Query),
        ]
    );
}

#[tokio::test]
async fn recall_ranks_score_then_confidence_then_id_without_dropping_zero_confidence() {
    let (service, _, embedding) = setup().await;
    for (key, confidence, values) in [
        (3, 0.8, [1.0, 0.0]),
        (1, 0.0, [1.0, 0.0]),
        (4, 1.0, [0.0, 1.0]),
        (2, 0.8, [1.0, 0.0]),
    ] {
        embedding.respond_next(Ok(embedding.result(values)));
        service
            .remember(
                id(key),
                Scope::Global,
                MemoryInput {
                    confidence: Confidence::try_from(confidence).unwrap(),
                    ..input(format!("Candidate {key}"))
                },
            )
            .await
            .unwrap();
    }
    // Include the entire candidate set; confidence only ranks retrieved hits.
    let hits = service
        .recall("Query", vec![Scope::Global], size(4))
        .await
        .unwrap();
    assert_eq!(
        hits.iter()
            .map(|hit| hit.record.memory.id)
            .collect::<Vec<_>>(),
        [id(2), id(3), id(1), id(4)]
    );
    assert_eq!(hits[0].score, hits[1].score);
    assert_eq!(hits[1].score, hits[2].score);
    assert!(hits[2].score > hits[3].score);
    assert_eq!(hits[2].record.memory.confidence.as_f32(), 0.0);
}
