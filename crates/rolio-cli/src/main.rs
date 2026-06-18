//! rolio: one CLI for humans and agents. `--json` is the only machine output.

mod client;
mod config;
mod output;

use clap::{Parser, Subcommand};
use client::{Client, Condition};
use output::Output;
use serde_json::json;
use uuid::Uuid;

#[derive(Parser)]
#[command(
    name = "rolio",
    version,
    about = "Explicit memory for people and agents"
)]
struct Cli {
    /// Server URL (default: env ROLO_URL, then the user config file)
    #[arg(long, global = true)]
    url: Option<String>,

    /// Bearer token (default: env ROLO_TOKEN, then the user config file)
    #[arg(long, global = true)]
    token: Option<String>,

    /// Request timeout in seconds
    #[arg(long, global = true)]
    timeout: Option<u64>,

    /// Print the machine-readable JSON envelope
    #[arg(long, global = true)]
    json: bool,

    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand)]
enum Command {
    /// Save a new memory; content arrives on stdin
    Remember {
        /// Scope: "global" or "project:<key>"
        #[arg(long)]
        scope: String,

        /// Memory category
        #[arg(long, default_value = "note")]
        kind: String,

        /// Confidence from 0.0 to 1.0 in tenths
        #[arg(long)]
        confidence: Option<f32>,

        /// Optional explicit ID; generated when omitted
        #[arg(long)]
        id: Option<Uuid>,
    },
    /// Semantic search across selected scopes
    Recall {
        query: String,

        /// Scope to search; repeat for several
        #[arg(long = "scope", required = true)]
        scopes: Vec<String>,

        /// Maximum number of results
        #[arg(long, default_value_t = 5)]
        limit: usize,
    },
    /// Read one memory by ID
    Get { id: Uuid },
    /// List memories in one scope
    List {
        /// Scope: "global" or "project:<key>"
        #[arg(long)]
        scope: String,

        /// Page size
        #[arg(long, default_value_t = 50)]
        limit: usize,

        /// Cursor from a previous page
        #[arg(long)]
        cursor: Option<String>,
    },
    /// Replace a memory; content arrives on stdin
    Update {
        id: Uuid,

        /// Expected revision
        #[arg(long)]
        revision: String,

        /// Memory category
        #[arg(long, default_value = "note")]
        kind: String,

        /// Confidence from 0.0 to 1.0 in tenths
        #[arg(long)]
        confidence: Option<f32>,
    },
    /// Delete a memory if the revision still matches
    Forget {
        id: Uuid,

        /// Expected revision
        #[arg(long)]
        revision: String,
    },
}

fn main() {
    let cli = Cli::parse();
    let output = Output::from_flag(cli.json);
    let config = match config::resolve(cli.url.as_deref(), cli.token.as_deref(), cli.timeout) {
        Ok(config) => config,
        Err(error) => {
            eprintln!("error: {error}");
            std::process::exit(client::exit_code::USAGE);
        }
    };
    let client = Client::new(&config);
    let outcome = match run(&client, &cli.command) {
        Ok(outcome) => outcome,
        Err(message) => {
            // Transport-level failure of a write: the outcome may still have
            // committed. Do not retry; explain how to confirm.
            eprintln!("error: {message}");
            if writes(&cli.command) {
                eprintln!(
                    "The write outcome is unknown. Read the memory back with `rolio get` \
                     before writing again; do not retry blindly."
                );
            }
            std::process::exit(client::exit_code::CONNECTION);
        }
    };
    if (200..300).contains(&outcome.status) {
        output.success(&outcome.body);
    } else {
        output.failure(&outcome.body, outcome.exit_code());
    }
}

fn writes(command: &Command) -> bool {
    matches!(
        command,
        Command::Remember { .. } | Command::Update { .. } | Command::Forget { .. }
    )
}

fn run(client: &Client, command: &Command) -> Result<client::Outcome, String> {
    match command {
        Command::Remember {
            scope,
            kind,
            confidence,
            id,
        } => {
            let content = client::read_stdin()?;
            client.put(
                id.unwrap_or_else(Uuid::new_v4),
                Condition::Create,
                &json!({
                    "scope": scope,
                    "content": content,
                    "kind": kind,
                    "confidence": confidence,
                }),
            )
        }
        Command::Recall {
            query,
            scopes,
            limit,
        } => client.search(&json!({
            "query": query,
            "scopes": scopes,
            "limit": limit,
        })),
        Command::Get { id } => client.get(&format!("/v1/memories/{id}")),
        Command::List {
            scope,
            limit,
            cursor,
        } => {
            let mut query = format!("scope={}&limit={limit}", urlencoding(scope));
            if let Some(cursor) = cursor {
                query.push_str(&format!("&cursor={}", urlencoding(cursor)));
            }
            client.list(&query)
        }
        Command::Update {
            id,
            revision,
            kind,
            confidence,
        } => {
            let content = client::read_stdin()?;
            client.put(
                *id,
                Condition::Update(revision.clone()),
                &json!({
                    "content": content,
                    "kind": kind,
                    "confidence": confidence,
                }),
            )
        }
        Command::Forget { id, revision } => client.delete(*id, revision),
    }
}

/// Minimal percent-encoding for query values without another dependency.
fn urlencoding(value: &str) -> String {
    let mut encoded = String::with_capacity(value.len());
    for byte in value.as_bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                encoded.push(*byte as char);
            }
            _ => encoded.push_str(&format!("%{byte:02X}")),
        }
    }
    encoded
}
