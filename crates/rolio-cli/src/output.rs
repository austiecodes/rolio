//! Output shaping: one JSON object on stdout in --json mode, short human
//! lines otherwise; diagnostics always on stderr.

use serde_json::Value;

pub enum Output {
    Json,
    Human,
}

impl Output {
    pub fn from_flag(json: bool) -> Self {
        if json { Self::Json } else { Self::Human }
    }

    /// Machine mode prints the server envelope verbatim: the wire format is
    /// the only parsing surface, never the human rendering.
    pub fn success(&self, envelope: &Value) {
        match self {
            Self::Json => println!("{envelope}"),
            Self::Human => println!("{}", human(envelope)),
        }
    }

    pub fn failure(&self, envelope: &Value, exit: i32) -> ! {
        match self {
            Self::Json => {
                eprintln!("{envelope}");
                std::process::exit(exit);
            }
            Self::Human => {
                let code = envelope["error"]["code"].as_str().unwrap_or("error");
                let message = envelope["error"]["message"]
                    .as_str()
                    .unwrap_or("The request failed.");
                eprintln!("error [{code}]: {message}");
                std::process::exit(exit);
            }
        }
    }
}

fn human(envelope: &Value) -> String {
    let data = &envelope["data"];
    if let Some(results) = data["results"].as_array() {
        return format!(
            "{} result{}",
            results.len(),
            if results.len() == 1 { "" } else { "s" }
        ) + &results
            .iter()
            .map(|hit| {
                format!(
                    "\n{}  {:?}  {}",
                    hit["id"].as_str().unwrap_or("?"),
                    hit["score"].as_f64().unwrap_or_default(),
                    first_line(hit["content"].as_str().unwrap_or(""))
                )
            })
            .collect::<String>();
    }
    if let Some(memories) = data["memories"].as_array() {
        let mut text = format!(
            "{} memor{}",
            memories.len(),
            if memories.len() == 1 { "y" } else { "ies" }
        );
        for memory in memories {
            text.push_str(&format!(
                "\n{}  [{}] {}",
                memory["id"].as_str().unwrap_or("?"),
                memory["scope"].as_str().unwrap_or("?"),
                first_line(memory["content"].as_str().unwrap_or(""))
            ));
        }
        if let Some(next) = data["next"].as_str() {
            text.push_str(&format!("\nnext cursor: {next}"));
        }
        return text;
    }
    if !data["id"].is_null() {
        return format!(
            "{} revision {}",
            data["id"].as_str().unwrap_or("?"),
            data["revision"].as_str().unwrap_or("?")
        );
    }
    if !data["deleted"].is_null() {
        return String::from("deleted");
    }
    if !data["status"].is_null() {
        return data["status"].as_str().unwrap_or("ok").to_owned();
    }
    serde_json::to_string(data).unwrap_or_else(|_| String::from("done"))
}

fn first_line(text: &str) -> String {
    let mut line = text
        .lines()
        .find(|line| !line.trim().is_empty())
        .unwrap_or("")
        .to_owned();
    if line.chars().count() > 72 {
        let truncated: String = line.chars().take(69).collect();
        line = format!("{truncated}...");
    }
    line.replace('\t', " ")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::client::{Outcome, exit_code};

    #[test]
    fn exit_codes_follow_the_documented_contract() {
        let outcome = |status: u16, code: &str| Outcome {
            status,
            body: serde_json::json!({ "error": { "code": code } }),
        };
        assert_eq!(outcome(200, "").exit_code(), exit_code::SUCCESS);
        assert_eq!(
            outcome(400, "invalid_input").exit_code(),
            exit_code::INVALID_INPUT
        );
        assert_eq!(
            outcome(400, "token_limit").exit_code(),
            exit_code::INVALID_INPUT
        );
        assert_eq!(
            outcome(401, "unauthorized").exit_code(),
            exit_code::UNAUTHORIZED
        );
        assert_eq!(
            outcome(412, "revision_conflict").exit_code(),
            exit_code::CONFLICT
        );
        assert_eq!(
            outcome(428, "precondition_required").exit_code(),
            exit_code::USAGE
        );
        assert_eq!(
            outcome(503, "outcome_unknown").exit_code(),
            exit_code::UNKNOWN_OUTCOME
        );
        assert_eq!(
            outcome(500, "internal").exit_code(),
            exit_code::SERVER_ERROR
        );
    }

    #[test]
    fn human_output_keeps_one_line_per_memory() {
        let envelope = serde_json::json!({
            "version": 1,
            "data": { "memories": [
                { "id": "a", "scope": "global", "content": "first\nsecond line" },
                { "id": "b", "scope": "project:x", "content": "  \nsecond memory" }
            ]}
        });
        let text = human(&envelope);
        assert!(text.starts_with("2 memories"));
        assert!(text.contains("a  [global] first"));
        assert!(text.contains("b  [project:x] second memory"));
    }

    #[test]
    fn long_content_is_truncated_at_seventy_two_columns() {
        let envelope = serde_json::json!({
            "version": 1,
            "data": { "memories": [
                { "id": "c", "scope": "global", "content": "y".repeat(200) }
            ]}
        });
        assert!(human(&envelope).lines().nth(1).unwrap().len() <= 90);
    }
}
