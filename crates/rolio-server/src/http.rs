//! HTTP v1: the only transport clients get. One bearer token, one protocol
//! version, stable error codes. Conditions travel as `If-None-Match: *`
//! (create) and `If-Match: <revision>` (update, delete).

use std::sync::Arc;

use axum::{
    Router,
    body::Bytes,
    extract::{DefaultBodyLimit, Path, Query, State},
    http::{HeaderMap, StatusCode, header},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD as BASE64;
use rolio_core::{Confidence, MemoryId, MemoryKind, Scope, Source};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use time::OffsetDateTime;

use crate::{
    embedding::EmbeddingError,
    memory::{MemoryInput, MemoryService},
    store::{ListCursor, ListRequest, Revision, StoreError, StoredMemory},
};

pub const PROTOCOL_VERSION: u32 = 1;
const MAX_BODY_BYTES: usize = 64 * 1024;
const DEFAULT_LIST_LIMIT: usize = 50;

/// Shared, immutable runtime state. The token is stored as a digest so a core
/// dump never contains the raw secret; comparisons stay length-independent.
pub struct AppState {
    pub service: MemoryService,
    pub backend: &'static str,
    token_digest: [u8; 32],
}

impl AppState {
    pub fn new(service: MemoryService, backend: &'static str, token: &str) -> Self {
        let token_digest = Sha256::digest(token.as_bytes()).into();
        Self {
            service,
            backend,
            token_digest,
        }
    }
}

pub fn router(state: Arc<AppState>) -> Router {
    Router::new()
        .route("/healthz", get(healthz))
        .route("/readyz", get(readyz))
        .route("/v1/info", get(info))
        .route(
            "/v1/memories/{id}",
            get(get_memory).put(put_memory).delete(delete_memory),
        )
        .route("/v1/memories", get(list_memories))
        .route("/v1/search", post(search))
        .layer(DefaultBodyLimit::max(MAX_BODY_BYTES))
        .with_state(state)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

async fn healthz() -> Response {
    data(serde_json::json!({ "status": "ok" }))
}

async fn readyz(State(state): State<Arc<AppState>>) -> Response {
    match state.service.check().await {
        Ok(()) => data(serde_json::json!({ "status": "ready" })),
        Err(_) => ApiError::unavailable("The server is not ready.").into_response(),
    }
}

async fn info(State(state): State<Arc<AppState>>, headers: HeaderMap) -> Response {
    if let Err(error) = authorize(&state, &headers) {
        return error.into_response();
    }
    let profile = state.service.profile();
    data(serde_json::json!({
        "version": PROTOCOL_VERSION,
        "store": state.backend,
        "embedding": {
            "model": profile.model,
            "dimensions": profile.dimensions.get(),
            "artifact_digest": hex(&profile.artifact_digest),
            "distance": "cosine",
        },
    }))
}

#[derive(Deserialize)]
struct PutMemoryBody {
    scope: Option<String>,
    content: String,
    kind: String,
    confidence: Option<f32>,
    source: Option<SourceBody>,
}

#[derive(Deserialize, Serialize, Default)]
struct SourceBody {
    uri: Option<String>,
    agent: Option<String>,
    external_reference: Option<String>,
}

async fn put_memory(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    Path(id): Path<String>,
    body: Bytes,
) -> Response {
    if let Err(error) = authorize(&state, &headers) {
        return error.into_response();
    }
    let Ok(id) = parse_id(&id) else {
        return ApiError::invalid_input("The memory ID is not a UUID.").into_response();
    };
    let input = match parse_body::<PutMemoryBody>(&body) {
        Ok(input) => input,
        Err(error) => return error.into_response(),
    };
    let memory_input = match memory_input(&input) {
        Ok(memory_input) => memory_input,
        Err(error) => return error.into_response(),
    };
    // A condition header is mandatory: no unconditional overwrite exists.
    let (create, expected) = match (
        condition(&headers, header::IF_NONE_MATCH),
        condition(&headers, header::IF_MATCH),
    ) {
        (Some(value), _) if value == "*" => (true, None),
        (_, Some(revision)) => match revision.parse::<Revision>() {
            Ok(revision) => (false, Some(revision)),
            Err(_) => {
                return ApiError::invalid_input("If-Match is not a revision UUID.").into_response();
            }
        },
        _ => {
            return ApiError::precondition_required(
                "Send If-None-Match: * to create or If-Match: <revision> to update.",
            )
            .into_response();
        }
    };
    let result = if create {
        let Some(scope_text) = input.scope.as_deref() else {
            return ApiError::invalid_input("A new memory requires a scope.").into_response();
        };
        let scope = match parse_scope(scope_text) {
            Ok(scope) => scope,
            Err(error) => return error.into_response(),
        };
        state
            .service
            .remember(id, scope, memory_input)
            .await
            .map(|record| (record, true))
    } else {
        state
            .service
            .update(id, expected.expect("checked above"), memory_input)
            .await
            .map(|record| (record, false))
    };
    match result {
        Ok((record, created)) => memory_response(
            &record,
            if created {
                StatusCode::CREATED
            } else {
                StatusCode::OK
            },
        ),
        Err(error) => ApiError::from_memory(error).into_response(),
    }
}

async fn get_memory(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    Path(id): Path<String>,
) -> Response {
    if let Err(error) = authorize(&state, &headers) {
        return error.into_response();
    }
    let Ok(id) = parse_id(&id) else {
        return ApiError::invalid_input("The memory ID is not a UUID.").into_response();
    };
    match state.service.get(id).await {
        Ok(record) => memory_response(&record, StatusCode::OK),
        Err(error) => ApiError::from_memory(error).into_response(),
    }
}

async fn delete_memory(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    Path(id): Path<String>,
) -> Response {
    if let Err(error) = authorize(&state, &headers) {
        return error.into_response();
    }
    let Ok(id) = parse_id(&id) else {
        return ApiError::invalid_input("The memory ID is not a UUID.").into_response();
    };
    let Some(revision) = condition(&headers, header::IF_MATCH) else {
        return ApiError::precondition_required("Send If-Match: <revision> to delete.")
            .into_response();
    };
    let Ok(revision) = revision.parse::<Revision>() else {
        return ApiError::invalid_input("If-Match is not a revision UUID.").into_response();
    };
    match state.service.forget(id, revision).await {
        Ok(()) => data(serde_json::json!({ "deleted": id.to_string() })),
        Err(error) => ApiError::from_memory(error).into_response(),
    }
}

#[derive(Deserialize)]
struct ListParams {
    scope: String,
    cursor: Option<String>,
    limit: Option<usize>,
}

async fn list_memories(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    Query(params): Query<ListParams>,
) -> Response {
    if let Err(error) = authorize(&state, &headers) {
        return error.into_response();
    }
    let scope = match parse_scope(&params.scope) {
        Ok(scope) => scope,
        Err(error) => return error.into_response(),
    };
    let limit = params.limit.unwrap_or(DEFAULT_LIST_LIMIT);
    if limit == 0 || limit > crate::memory::MemoryService::MAX_RESULTS {
        return ApiError::invalid_input("limit must be between 1 and 100.").into_response();
    }
    let cursor = match params.cursor.as_deref() {
        None => None,
        Some(text) => match decode_cursor(text) {
            Ok(cursor) if cursor.scope == scope => Some(cursor),
            _ => {
                return ApiError::invalid_input("The cursor is invalid or bound to another scope.")
                    .into_response();
            }
        },
    };
    match state
        .service
        .list(ListRequest {
            scope,
            cursor,
            limit: std::num::NonZeroUsize::new(limit).expect("checked above"),
        })
        .await
    {
        Ok(page) => data(ListResponse {
            memories: page
                .records
                .iter()
                .map(MemoryJson::from)
                .collect::<Vec<_>>(),
            next: page.next.map(|cursor| encode_cursor(&cursor)),
        }),
        Err(error) => ApiError::from_memory(error).into_response(),
    }
}

#[derive(Serialize)]
struct ListResponse {
    memories: Vec<MemoryJson>,
    next: Option<String>,
}

#[derive(Deserialize)]
struct SearchBody {
    query: String,
    scopes: Vec<String>,
    limit: Option<usize>,
}

async fn search(State(state): State<Arc<AppState>>, headers: HeaderMap, body: Bytes) -> Response {
    if let Err(error) = authorize(&state, &headers) {
        return error.into_response();
    }
    let request = match parse_body::<SearchBody>(&body) {
        Ok(request) => request,
        Err(error) => return error.into_response(),
    };
    let mut scopes = Vec::with_capacity(request.scopes.len());
    for text in &request.scopes {
        match parse_scope(text) {
            Ok(scope) => scopes.push(scope),
            Err(error) => return error.into_response(),
        }
    }
    let limit = request.limit.unwrap_or(10);
    if limit == 0 || limit > crate::memory::MemoryService::MAX_RESULTS {
        return ApiError::invalid_input("limit must be between 1 and 100.").into_response();
    }
    match state
        .service
        .recall(
            &request.query,
            scopes,
            std::num::NonZeroUsize::new(limit).expect("checked above"),
        )
        .await
    {
        Ok(hits) => data(serde_json::json!({
            "results": hits
                .iter()
                .map(|hit| {
                    let mut item = MemoryJson::from(&hit.record);
                    item.score = Some(hit.score);
                    item
                })
                .collect::<Vec<_>>(),
        })),
        Err(error) => ApiError::from_memory(error).into_response(),
    }
}

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

#[derive(Serialize)]
struct MemoryJson {
    id: String,
    scope: String,
    kind: String,
    content: String,
    confidence: f64,
    source: Option<SourceBody>,
    #[serde(with = "time::serde::rfc3339")]
    created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    updated_at: OffsetDateTime,
    revision: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    score: Option<f32>,
}

impl From<&StoredMemory> for MemoryJson {
    fn from(record: &StoredMemory) -> Self {
        Self {
            id: record.memory.id.to_string(),
            scope: scope_text(&record.memory.scope),
            kind: kind_text(record.memory.kind).to_string(),
            content: record.memory.content.clone(),
            confidence: f64::from(record.memory.confidence.tenths()) / 10.0,
            source: record.memory.source.as_ref().map(|source| SourceBody {
                uri: source.uri.clone(),
                agent: source.agent.clone(),
                external_reference: source.external_reference.clone(),
            }),
            created_at: record.memory.created_at,
            updated_at: record.memory.updated_at,
            revision: record.revision.to_string(),
            score: None,
        }
    }
}

fn memory_response(record: &StoredMemory, status: StatusCode) -> Response {
    let mut response = data(MemoryJson::from(record));
    *response.status_mut() = status;
    let etag = format!("\"{}\"", record.revision);
    // A generated header value cannot fail to parse; do not panic in handlers.
    if let Ok(value) = etag.parse() {
        response.headers_mut().insert(header::ETAG, value);
    }
    response
}

fn data<T: Serialize>(payload: T) -> Response {
    let body = serde_json::json!({ "version": PROTOCOL_VERSION, "data": payload });
    (StatusCode::OK, axum::Json(body)).into_response()
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

struct ApiError {
    status: StatusCode,
    code: &'static str,
    message: String,
}

impl ApiError {
    fn invalid_input(message: impl Into<String>) -> Self {
        Self {
            status: StatusCode::BAD_REQUEST,
            code: "invalid_input",
            message: message.into(),
        }
    }

    fn unauthorized() -> Self {
        Self {
            status: StatusCode::UNAUTHORIZED,
            code: "unauthorized",
            message: String::from("A valid bearer token is required."),
        }
    }

    fn precondition_required(message: impl Into<String>) -> Self {
        Self {
            status: StatusCode::PRECONDITION_REQUIRED,
            code: "precondition_required",
            message: message.into(),
        }
    }

    fn unavailable(message: impl Into<String>) -> Self {
        Self {
            status: StatusCode::SERVICE_UNAVAILABLE,
            code: "unavailable",
            message: message.into(),
        }
    }

    fn from_memory(error: crate::memory::MemoryError) -> Self {
        use crate::memory::MemoryError;
        match error {
            MemoryError::InvalidInput => {
                Self::invalid_input("The input exceeds the supported limits.")
            }
            MemoryError::NotFound => Self {
                status: StatusCode::NOT_FOUND,
                code: "not_found",
                message: String::from("The memory does not exist."),
            },
            MemoryError::Embedding(embedding) => Self::from_embedding(embedding),
            MemoryError::Store(store) => Self::from_store(store),
        }
    }

    fn from_embedding(error: EmbeddingError) -> Self {
        let (status, code) = match error {
            EmbeddingError::InvalidInput => (StatusCode::BAD_REQUEST, "invalid_input"),
            EmbeddingError::TokenLimit => (StatusCode::BAD_REQUEST, "token_limit"),
            EmbeddingError::Busy => (StatusCode::SERVICE_UNAVAILABLE, "embedding_busy"),
            EmbeddingError::Timeout => (StatusCode::GATEWAY_TIMEOUT, "embedding_timeout"),
            EmbeddingError::Unavailable => {
                (StatusCode::SERVICE_UNAVAILABLE, "embedding_unavailable")
            }
            EmbeddingError::IncompatibleProfile => {
                (StatusCode::SERVICE_UNAVAILABLE, "embedding_incompatible")
            }
            EmbeddingError::InvalidVector => (StatusCode::SERVICE_UNAVAILABLE, "embedding_invalid"),
        };
        Self {
            status,
            code,
            message: error.to_string(),
        }
    }

    fn from_store(error: StoreError) -> Self {
        let (status, code, message) = match error {
            StoreError::AlreadyExists => (
                StatusCode::PRECONDITION_FAILED,
                "already_exists",
                String::from("The memory ID already exists."),
            ),
            StoreError::RevisionConflict => (
                StatusCode::PRECONDITION_FAILED,
                "revision_conflict",
                String::from("The memory changed. Read it again before you write it."),
            ),
            StoreError::ScopeChange => (
                StatusCode::PRECONDITION_FAILED,
                "scope_change",
                String::from("An update cannot change the memory scope."),
            ),
            StoreError::InvalidMemory
            | StoreError::InvalidVector
            | StoreError::DimensionMismatch => {
                (StatusCode::BAD_REQUEST, "invalid_input", error.to_string())
            }
            StoreError::MissingScope | StoreError::CursorMismatch => {
                (StatusCode::BAD_REQUEST, "invalid_input", error.to_string())
            }
            StoreError::IncompatibleIdentity => (
                StatusCode::SERVICE_UNAVAILABLE,
                "store_incompatible",
                String::from("The store schema or embedding profile does not match this server."),
            ),
            StoreError::ResourceLimit => (
                StatusCode::PAYLOAD_TOO_LARGE,
                "resource_limit",
                error.to_string(),
            ),
            StoreError::Unavailable => (
                StatusCode::SERVICE_UNAVAILABLE,
                "store_unavailable",
                error.to_string(),
            ),
            StoreError::OutcomeUnknown => (
                StatusCode::SERVICE_UNAVAILABLE,
                "outcome_unknown",
                String::from(
                    "The write outcome is unknown. Read the memory back before another write.",
                ),
            ),
        };
        Self {
            status,
            code,
            message,
        }
    }
}

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        let body = serde_json::json!({
            "version": PROTOCOL_VERSION,
            "error": { "code": self.code, "message": self.message },
        });
        (self.status, axum::Json(body)).into_response()
    }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

fn authorize(state: &AppState, headers: &HeaderMap) -> Result<(), ApiError> {
    let presented = headers
        .get(header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
        .ok_or_else(ApiError::unauthorized)?;
    let digest: [u8; 32] = Sha256::digest(presented.as_bytes()).into();
    if digest == state.token_digest {
        Ok(())
    } else {
        Err(ApiError::unauthorized())
    }
}

fn parse_id(text: &str) -> Result<MemoryId, ()> {
    text.parse().map_err(|_| ())
}

fn parse_body<T: for<'de> Deserialize<'de>>(body: &[u8]) -> Result<T, ApiError> {
    serde_json::from_slice(body)
        .map_err(|_| ApiError::invalid_input("The request body is not valid JSON."))
}

fn memory_input(body: &PutMemoryBody) -> Result<MemoryInput, ApiError> {
    let kind = parse_kind(&body.kind)?;
    let confidence = match body.confidence {
        None => Confidence::default(),
        Some(value) => Confidence::try_from(value)
            .map_err(|_| ApiError::invalid_input("confidence must be 0.0 to 1.0 in tenths."))?,
    };
    Ok(MemoryInput {
        content: body.content.clone(),
        kind,
        confidence,
        source: body.source.as_ref().map(|source| Source {
            uri: source.uri.clone(),
            agent: source.agent.clone(),
            external_reference: source.external_reference.clone(),
        }),
    })
}

fn parse_kind(text: &str) -> Result<MemoryKind, ApiError> {
    match text {
        "note" => Ok(MemoryKind::Note),
        "preference" => Ok(MemoryKind::Preference),
        "convention" => Ok(MemoryKind::Convention),
        "fact" => Ok(MemoryKind::Fact),
        "solution" => Ok(MemoryKind::Solution),
        "lesson" => Ok(MemoryKind::Lesson),
        "plan" => Ok(MemoryKind::Plan),
        _ => Err(ApiError::invalid_input(
            "kind must be one of the seven memory kinds.",
        )),
    }
}

fn kind_text(kind: MemoryKind) -> &'static str {
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

fn parse_scope(text: &str) -> Result<Scope, ApiError> {
    if text == "global" {
        return Ok(Scope::Global);
    }
    let key = text
        .strip_prefix("project:")
        .ok_or_else(|| ApiError::invalid_input("scope must be 'global' or 'project:<key>'."))?;
    Ok(Scope::Project(rolio_core::ProjectKey::new(key).map_err(
        |_| ApiError::invalid_input("The project key is not valid."),
    )?))
}

fn scope_text(scope: &Scope) -> String {
    match scope {
        Scope::Global => String::from("global"),
        Scope::Project(key) => format!("project:{}", key.as_str()),
    }
}

fn condition(headers: &HeaderMap, name: header::HeaderName) -> Option<String> {
    headers
        .get(&name)
        .and_then(|value| value.to_str().ok())
        .map(str::trim)
        .map(|value| value.trim_matches('"').to_string())
}

fn decode_cursor(text: &str) -> Result<ListCursor, ()> {
    let bytes = BASE64.decode(text).map_err(|_| ())?;
    let cursor: (String, String) = serde_json::from_slice(&bytes).map_err(|_| ())?;
    let after = cursor.1.parse().map_err(|_| ())?;
    let scope = parse_scope(&cursor.0).map_err(|_| ())?;
    Ok(ListCursor { scope, after })
}

fn encode_cursor(cursor: &ListCursor) -> String {
    let pair = (scope_text(&cursor.scope), cursor.after.to_string());
    BASE64.encode(serde_json::to_vec(&pair).expect("cursor serializes"))
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

#[cfg(test)]
mod tests;
