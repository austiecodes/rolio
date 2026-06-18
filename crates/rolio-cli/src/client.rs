//! HTTP client for protocol v1. Writes never retry: an uncertain outcome is
//! reported as such, with the ID and revision to read back.

use std::io::Read;

use serde_json::Value;
use uuid::Uuid;

use crate::config::Config;

/// Exit codes are part of the CLI contract; adapters must map, not invent.
pub mod exit_code {
    pub const SUCCESS: i32 = 0;
    pub const SERVER_ERROR: i32 = 1;
    pub const USAGE: i32 = 2;
    pub const INVALID_INPUT: i32 = 3;
    pub const CONFLICT: i32 = 4;
    pub const CONNECTION: i32 = 5;
    pub const UNAUTHORIZED: i32 = 6;
    pub const UNKNOWN_OUTCOME: i32 = 7;
}

#[derive(Debug)]
pub struct Outcome {
    pub status: u16,
    pub body: Value,
}

impl Outcome {
    pub fn error_code(&self) -> Option<&str> {
        self.body["error"]["code"].as_str()
    }

    pub fn exit_code(&self) -> i32 {
        match (self.status, self.error_code()) {
            (200..=299, _) => exit_code::SUCCESS,
            (400, Some("invalid_input")) | (400, Some("token_limit")) => exit_code::INVALID_INPUT,
            (401, _) => exit_code::UNAUTHORIZED,
            (412, _) => exit_code::CONFLICT,
            (428, _) => exit_code::USAGE,
            (503, Some("outcome_unknown")) => exit_code::UNKNOWN_OUTCOME,
            (200..=499, _) => exit_code::SERVER_ERROR,
            (500..=599, _) => exit_code::SERVER_ERROR,
            _ => exit_code::CONNECTION,
        }
    }
}

pub struct Client {
    http: reqwest::blocking::Client,
    base_url: String,
    token: String,
}

impl Client {
    pub fn new(config: &Config) -> Self {
        Self {
            http: reqwest::blocking::Client::builder()
                .timeout(config.timeout)
                .build()
                .expect("reqwest client builds"),
            base_url: config.url.trim_end_matches('/').to_owned(),
            token: config.token.clone(),
        }
    }

    /// Reads retry once on connection failure; bounded and safe.
    pub fn get(&self, path: &str) -> Result<Outcome, String> {
        self.request_with_retry(reqwest::Method::GET, path, None, vec![])
    }

    pub fn list(&self, query: &str) -> Result<Outcome, String> {
        self.get(&format!("/v1/memories?{query}"))
    }

    /// Writes execute exactly once.
    pub fn put(&self, id: Uuid, condition: Condition, body: &Value) -> Result<Outcome, String> {
        let header = match condition {
            Condition::Create => ("If-None-Match", "*".to_string()),
            Condition::Update(revision) => ("If-Match", revision),
        };
        self.request_with_retry(
            reqwest::Method::PUT,
            &format!("/v1/memories/{id}"),
            Some(body),
            vec![header],
        )
        // A connection-level failure here means the outcome is unknown; the
        // caller prints the read-back guidance instead of retrying.
    }

    pub fn delete(&self, id: Uuid, revision: &str) -> Result<Outcome, String> {
        self.request_once(
            reqwest::Method::DELETE,
            &format!("/v1/memories/{id}"),
            None,
            vec![("If-Match", revision.to_string())],
        )
    }

    pub fn search(&self, body: &Value) -> Result<Outcome, String> {
        self.request_once(reqwest::Method::POST, "/v1/search", Some(body), vec![])
    }

    fn request_with_retry(
        &self,
        method: reqwest::Method,
        path: &str,
        body: Option<&Value>,
        headers: Vec<(&str, String)>,
    ) -> Result<Outcome, String> {
        match self.request_once(method.clone(), path, body, headers.clone()) {
            Err(_) if method == reqwest::Method::GET => {
                self.request_once(method, path, body, headers)
            }
            other => other,
        }
    }

    fn request_once(
        &self,
        method: reqwest::Method,
        path: &str,
        body: Option<&Value>,
        headers: Vec<(&str, String)>,
    ) -> Result<Outcome, String> {
        let mut request = self
            .http
            .request(method, format!("{}{path}", self.base_url))
            .bearer_auth(&self.token);
        for (name, value) in headers {
            request = request.header(name, value);
        }
        let request = match body {
            Some(body) => request.json(body),
            None => request,
        };
        let response = request.send().map_err(|error| error.to_string())?;
        let status = response.status().as_u16();
        let body: Value = response.json().unwrap_or(Value::Null);
        Ok(Outcome { status, body })
    }
}

pub enum Condition {
    Create,
    Update(String),
}

/// Content always travels over stdin: no shell quoting, no process listing.
pub fn read_stdin() -> Result<String, String> {
    let mut content = String::new();
    std::io::stdin()
        .read_to_string(&mut content)
        .map_err(|error| format!("cannot read content from stdin: {error}"))?;
    Ok(content)
}
