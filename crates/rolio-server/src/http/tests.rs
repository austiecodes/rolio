use std::sync::Arc;

use async_trait::async_trait;
use axum::{
    Router,
    body::Body,
    http::{Request, StatusCode, header},
};
use rolio_core::MemoryId;
use tower::ServiceExt;

use super::*;
use crate::{
    embedding::{Embedding, EmbeddingError, EmbeddingProvider, InputKind},
    memory::MemoryService,
    store::{EmbeddingProfile, Vector, contracts, fake::FakeStore},
};

const TOKEN: &str = "test-token";

struct FixedProvider {
    profile: EmbeddingProfile,
}

#[async_trait]
impl EmbeddingProvider for FixedProvider {
    fn profile(&self) -> &EmbeddingProfile {
        &self.profile
    }

    async fn embed(&self, _: &str, _: InputKind) -> Result<Embedding, EmbeddingError> {
        Ok(Embedding {
            profile: self.profile.clone(),
            vector: Vector::new(vec![1.0, 0.0]).unwrap(),
        })
    }
}

async fn app() -> Router {
    let identity = contracts::identity();
    let store = Arc::new(FakeStore::new(identity.clone()));
    let provider = Arc::new(FixedProvider {
        profile: identity.embedding_profile.clone(),
    });
    let service = MemoryService::new(store, provider, identity).await.unwrap();
    router(Arc::new(AppState::new(service, "postgres", TOKEN)))
}

fn request(
    method: &str,
    uri: &str,
    token: Option<&str>,
    headers: &[(&str, &str)],
    body: &str,
) -> Request<Body> {
    let mut builder = Request::builder().method(method).uri(uri);
    if let Some(token) = token {
        builder = builder.header(header::AUTHORIZATION, format!("Bearer {token}"));
    }
    for (name, value) in headers {
        builder = builder.header(*name, *value);
    }
    builder.body(Body::from(body.to_string())).unwrap()
}

fn code(body: &[u8]) -> String {
    serde_json::from_slice::<serde_json::Value>(body).unwrap()["error"]["code"]
        .as_str()
        .unwrap()
        .to_string()
}

async fn json_body(response: axum::response::Response) -> serde_json::Value {
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .unwrap();
    serde_json::from_slice(&bytes).unwrap()
}

#[tokio::test]
async fn healthz_needs_no_token() {
    let response = app()
        .await
        .oneshot(request("GET", "/healthz", None, &[], "{}"))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
}

#[tokio::test]
async fn data_routes_reject_missing_and_wrong_tokens() {
    let app = app().await;
    for token in [None, Some("wrong"), Some("")] {
        let response = app
            .clone()
            .oneshot(request("GET", "/v1/info", token, &[], "{}"))
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
    }
    let response = app
        .oneshot(request("GET", "/v1/info", Some(TOKEN), &[], "{}"))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
}

#[tokio::test]
async fn writes_require_a_condition_header() {
    let app = app().await;
    let body = r#"{"scope":"global","content":"Body","kind":"note"}"#;
    let response = app
        .clone()
        .oneshot(request(
            "PUT",
            &format!("/v1/memories/{}", MemoryId::new()),
            Some(TOKEN),
            &[],
            body,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::PRECONDITION_REQUIRED);
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .unwrap();
    assert_eq!(code(&bytes), "precondition_required");
}

#[tokio::test]
async fn memory_lifecycle_over_http() {
    let app = app().await;
    let id = MemoryId::new();
    let body = r#"{"scope":"global","content":"First","kind":"note","confidence":0.8}"#;
    let response = app
        .clone()
        .oneshot(request(
            "PUT",
            &format!("/v1/memories/{id}"),
            Some(TOKEN),
            &[("if-none-match", "*")],
            body,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::CREATED);
    let etag = response
        .headers()
        .get(header::ETAG)
        .and_then(|value| value.to_str().ok())
        .expect("creation returns ETag")
        .trim_matches('"')
        .to_string();

    // A second create with the same ID is an explicit conflict.
    let response = app
        .clone()
        .oneshot(request(
            "PUT",
            &format!("/v1/memories/{id}"),
            Some(TOKEN),
            &[("if-none-match", "*")],
            body,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::PRECONDITION_FAILED);
    let duplicate = json_body(response).await;
    assert_eq!(duplicate["error"]["code"], "already_exists");
    assert_eq!(duplicate["error"]["current"]["revision"], etag);

    // Wrong revision cannot update; the conflict carries the current record.
    let other = Revision::new().to_string();
    let response = app
        .clone()
        .oneshot(request(
            "PUT",
            &format!("/v1/memories/{id}"),
            Some(TOKEN),
            &[("if-match", &other)],
            r#"{"content":"Second","kind":"note"}"#,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::PRECONDITION_FAILED);
    let conflict = json_body(response).await;
    assert_eq!(conflict["error"]["code"], "revision_conflict");
    assert_eq!(conflict["error"]["current"]["revision"], etag);
    assert_eq!(conflict["error"]["current"]["content"], "First");

    // Read carries the revision as ETag.
    let response = app
        .clone()
        .oneshot(request(
            "GET",
            &format!("/v1/memories/{id}"),
            Some(TOKEN),
            &[],
            "",
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .unwrap();
    let record: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(record["data"]["revision"], etag);
    assert_eq!(record["data"]["scope"], "global");
    assert_eq!(record["data"]["kind"], "note");
    assert!(
        record["data"]["created_at"]
            .as_str()
            .unwrap()
            .starts_with("20"),
        "timestamps serialize as RFC 3339 text"
    );

    // Conditional update publishes a new revision.
    let response = app
        .clone()
        .oneshot(request(
            "PUT",
            &format!("/v1/memories/{id}"),
            Some(TOKEN),
            &[("if-match", &etag)],
            r#"{"content":"Second","kind":"fact"}"#,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .unwrap();
    let updated: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    let new_etag = updated["data"]["revision"].as_str().unwrap().to_string();
    assert_ne!(new_etag, etag);

    // Delete needs its own condition.
    let response = app
        .clone()
        .oneshot(request(
            "DELETE",
            &format!("/v1/memories/{id}"),
            Some(TOKEN),
            &[],
            "",
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::PRECONDITION_REQUIRED);
    let response = app
        .clone()
        .oneshot(request(
            "DELETE",
            &format!("/v1/memories/{id}"),
            Some(TOKEN),
            &[("if-match", &etag)],
            "",
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::PRECONDITION_FAILED);
    let stale_delete = json_body(response).await;
    assert_eq!(stale_delete["error"]["current"]["revision"], new_etag);

    let response = app
        .clone()
        .oneshot(request(
            "DELETE",
            &format!("/v1/memories/{id}"),
            Some(TOKEN),
            &[("if-match", &new_etag)],
            "",
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);

    // A delete on a vanished record conflicts without a current record.
    let response = app
        .clone()
        .oneshot(request(
            "DELETE",
            &format!("/v1/memories/{id}"),
            Some(TOKEN),
            &[("if-match", &new_etag)],
            "",
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::PRECONDITION_FAILED);
    let vanished = json_body(response).await;
    assert_eq!(vanished["error"]["code"], "revision_conflict");
    assert!(vanished["error"]["current"].is_null());

    let response = app
        .oneshot(request(
            "GET",
            &format!("/v1/memories/{id}"),
            Some(TOKEN),
            &[],
            "",
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NOT_FOUND);
}

#[tokio::test]
async fn list_paginates_with_opaque_cursors() {
    let app = app().await;
    for index in 0..3 {
        let response = app
            .clone()
            .oneshot(request(
                "PUT",
                &format!("/v1/memories/{}", MemoryId::new()),
                Some(TOKEN),
                &[("if-none-match", "*")],
                &format!(r#"{{"scope":"project:web","content":"item {index}","kind":"note"}}"#),
            ))
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::CREATED);
    }
    let response = app
        .clone()
        .oneshot(request(
            "GET",
            "/v1/memories?scope=project:web&limit=2",
            Some(TOKEN),
            &[],
            "",
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .unwrap();
    let page: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(page["data"]["memories"].as_array().unwrap().len(), 2);
    let cursor = page["data"]["next"].as_str().unwrap().to_string();

    // The cursor is bound to its scope.
    let response = app
        .clone()
        .oneshot(request(
            "GET",
            "/v1/memories?scope=global&cursor={cursor}",
            Some(TOKEN),
            &[],
            "",
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);

    let response = app
        .clone()
        .oneshot(request(
            "GET",
            &format!("/v1/memories?scope=project:web&cursor={cursor}"),
            Some(TOKEN),
            &[],
            "",
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .unwrap();
    let page: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(page["data"]["memories"].as_array().unwrap().len(), 1);
    assert!(page["data"]["next"].is_null());
}

#[tokio::test]
async fn search_returns_scored_results() {
    let app = app().await;
    let response = app
        .clone()
        .oneshot(request(
            "PUT",
            &format!("/v1/memories/{}", MemoryId::new()),
            Some(TOKEN),
            &[("if-none-match", "*")],
            r#"{"scope":"project:web","content":"Rate limit the API","kind":"solution"}"#,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::CREATED);
    let response = app
        .clone()
        .oneshot(request(
            "POST",
            "/v1/search",
            Some(TOKEN),
            &[],
            r#"{"query":"how do I protect the API?","scopes":["project:web"],"limit":5}"#,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .unwrap();
    let result: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    let results = result["data"]["results"].as_array().unwrap();
    assert_eq!(results.len(), 1);
    assert!(results[0]["score"].as_f64().is_some());

    let response = app
        .oneshot(request(
            "POST",
            "/v1/search",
            Some(TOKEN),
            &[],
            r#"{"query":"q","scopes":[],"limit":5}"#,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
}

#[tokio::test]
async fn invalid_inputs_are_rejected_with_stable_codes() {
    let app = app().await;
    let id = MemoryId::new();
    for body in [
        "not json",
        r#"{"scope":"global","content":"x","kind":"novel"}"#,
        r#"{"scope":"global","content":"x","kind":"note","confidence":0.33}"#,
        r#"{"scope":"everywhere","content":"x","kind":"note"}"#,
    ] {
        let response = app
            .clone()
            .oneshot(request(
                "PUT",
                &format!("/v1/memories/{id}"),
                Some(TOKEN),
                &[("if-none-match", "*")],
                body,
            ))
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::BAD_REQUEST, "body: {body}");
        let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
            .await
            .unwrap();
        assert_eq!(code(&bytes), "invalid_input");
    }
    let response = app
        .oneshot(request(
            "PUT",
            "/v1/memories/not-a-uuid",
            Some(TOKEN),
            &[("if-none-match", "*")],
            r#"{"scope":"global","content":"x","kind":"note"}"#,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
}
