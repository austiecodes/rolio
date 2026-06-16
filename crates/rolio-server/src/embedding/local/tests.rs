use std::path::PathBuf;

use super::super::{EmbeddingError, EmbeddingProvider, InputKind, LocalEmbedding};

/// Real-model tests are ignored by default: they need the ~610 MB pinned
/// artifact installed by `scripts/setup-embedding.py`.
fn artifacts_directory() -> Option<PathBuf> {
    let directory = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../.native/embedding/qwen3-embedding-0.6b");
    directory.is_dir().then_some(directory)
}

fn open() -> Option<LocalEmbedding> {
    artifacts_directory().and_then(|directory| LocalEmbedding::open(&directory).ok())
}

#[test]
fn manifest_pins_one_exact_artifact() {
    let manifest = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("src/embedding/artifacts.json");
    let text = std::fs::read_to_string(manifest).unwrap();
    let parsed: serde_json::Value = serde_json::from_str(&text).unwrap();
    assert_eq!(parsed["model"], "Qwen/Qwen3-Embedding-0.6B");
    assert_eq!(
        parsed["revision"],
        "370f27d7550e0def9b39c1f16d3fbaa13aa67728"
    );
    let files = parsed["files"].as_array().unwrap();
    assert_eq!(files.len(), 1);
    assert_eq!(files[0]["name"], "Qwen3-Embedding-0.6B-Q8_0.gguf");
    assert_eq!(files[0]["sha256"].as_str().unwrap().len(), 64);
}

#[tokio::test]
#[ignore = "requires the pinned Qwen3 GGUF artifact"]
async fn real_model_embeds_documents_and_queries() {
    let worker = open().expect("artifacts installed");
    let profile = worker.profile().clone();
    assert_eq!(profile.dimensions.get(), 1024);
    assert_eq!(
        profile.model,
        "Qwen/Qwen3-Embedding-0.6B@370f27d7550e0def9b39c1f16d3fbaa13aa67728:Q8_0"
    );
    for kind in [InputKind::Document, InputKind::Query] {
        let embedding = worker
            .embed("Store memories with an explicit scope.", kind)
            .await
            .unwrap();
        assert_eq!(embedding.vector.as_slice().len(), 1024);
        let norm: f64 = embedding
            .vector
            .as_slice()
            .iter()
            .map(|value| f64::from(*value).powi(2))
            .sum();
        assert!(
            (norm - 1.0).abs() < 1e-3,
            "normalized output, got norm {norm}"
        );
    }
}

#[tokio::test]
#[ignore = "requires the pinned Qwen3 GGUF artifact"]
async fn real_model_retrieval_prefers_the_relevant_document() {
    let worker = open().expect("artifacts installed");
    let documents = [
        "Database migrations must be reversible and tested against production data.",
        "数据库迁移前必须备份，并在维护窗口内执行。",
        "The weekly team dinner is usually on Thursday.",
    ];
    let mut vectors = Vec::new();
    for document in documents {
        vectors.push(worker.embed(document, InputKind::Document).await.unwrap());
    }
    let query = worker
        .embed("如何安全地执行数据库迁移？", InputKind::Query)
        .await
        .unwrap();
    let mut ranked: Vec<(usize, f32)> = vectors
        .iter()
        .enumerate()
        .map(|(index, vector)| {
            let dot: f32 = vector
                .vector
                .as_slice()
                .iter()
                .zip(query.vector.as_slice())
                .map(|(left, right)| left * right)
                .sum();
            (index, dot)
        })
        .collect();
    ranked.sort_by(|left, right| right.1.total_cmp(&left.1));
    assert!(
        ranked[0].0 == 1 || ranked[1].0 == 1,
        "the migration memory should rank in the top two: {ranked:?}"
    );
}

#[tokio::test]
#[ignore = "requires the pinned Qwen3 GGUF artifact"]
async fn real_model_rejects_token_overflow_without_truncating() {
    let worker = open().expect("artifacts installed");
    // CJK text is roughly one token per character; this stays under the byte
    // limit but exceeds the token budget.
    let overflow: String = "超".repeat(4500);
    assert!(overflow.len() <= 16 * 1024);
    for kind in [InputKind::Document, InputKind::Query] {
        assert_eq!(
            worker.embed(&overflow, kind).await.unwrap_err(),
            EmbeddingError::TokenLimit
        );
    }
}
