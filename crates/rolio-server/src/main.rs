//! Rolio server entry point. Configuration comes from the environment; the
//! server owns the store and the local embedding model exclusively.

#[cfg(not(all(feature = "store-postgres", feature = "embedding-local")))]
fn main() {
    eprintln!("rolio-server requires features `store-postgres` and `embedding-local`");
    std::process::exit(1);
}

#[cfg(all(feature = "store-postgres", feature = "embedding-local"))]
mod run {
    use std::{net::SocketAddr, path::PathBuf, process::ExitCode, sync::Arc};

    use rolio_server::{
        embedding::{EmbeddingProvider, LocalEmbedding},
        http::{self, AppState},
        memory::MemoryService,
        store::{MemoryStore, StoreIdentity, postgres::PgStore},
    };

    const DEFAULT_ADDRESS: &str = "127.0.0.1:8080";
    const DEFAULT_ARTIFACTS: &str = ".native/embedding/qwen3-embedding-0.6b";

    pub async fn run() -> ExitCode {
        tracing_subscriber::fmt()
            .with_env_filter(
                tracing_subscriber::EnvFilter::try_from_default_env()
                    .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info")),
            )
            .init();

        let address: SocketAddr = std::env::var("ROLO_ADDR")
            .unwrap_or_else(|_| DEFAULT_ADDRESS.to_string())
            .parse()
            .expect("ROLO_ADDR must be a socket address");
        let Ok(token) = std::env::var("ROLO_TOKEN") else {
            eprintln!("ROLO_TOKEN must be set to the bearer token clients use");
            return ExitCode::FAILURE;
        };
        let Ok(database_url) = std::env::var("ROLO_DATABASE_URL") else {
            eprintln!("ROLO_DATABASE_URL must point at the PostgreSQL database");
            return ExitCode::FAILURE;
        };
        let artifacts = PathBuf::from(
            std::env::var("ROLO_ARTIFACTS").unwrap_or_else(|_| DEFAULT_ARTIFACTS.to_string()),
        );

        // Loading the model hashes ~610 MB and initializes llama.cpp; keep it
        // off the async executor. Startup fails closed: no fallback provider.
        let embedding = match tokio::task::spawn_blocking(move || LocalEmbedding::open(&artifacts))
            .await
            .expect("startup task runs")
        {
            Ok(embedding) => embedding,
            Err(error) => {
                eprintln!("local embedding is unavailable: {error}");
                return ExitCode::FAILURE;
            }
        };
        let identity = StoreIdentity {
            schema_version: 1,
            embedding_profile: embedding.profile().clone(),
        };
        let store = match PgStore::connect(&database_url, identity.clone()).await {
            Ok(store) => Arc::new(store),
            Err(error) => {
                eprintln!("PostgreSQL store is unavailable: {error}");
                return ExitCode::FAILURE;
            }
        };
        let service = match MemoryService::new(
            store.clone() as Arc<dyn MemoryStore>,
            Arc::new(embedding),
            identity,
        )
        .await
        {
            Ok(service) => service,
            Err(error) => {
                eprintln!("service initialization failed: {error}");
                return ExitCode::FAILURE;
            }
        };
        let state = Arc::new(AppState::new(service, "postgres", &token));

        let listener = match tokio::net::TcpListener::bind(address).await {
            Ok(listener) => listener,
            Err(error) => {
                eprintln!("cannot listen on {address}: {error}");
                return ExitCode::FAILURE;
            }
        };
        tracing::info!(%address, "rolio-server listening");
        let app = http::router(state);
        let server = axum::serve(listener, app).with_graceful_shutdown(shutdown_signal());
        match server.await {
            Ok(()) => {
                tracing::info!("shutdown complete");
                ExitCode::SUCCESS
            }
            Err(error) => {
                tracing::error!(%error, "server error");
                ExitCode::FAILURE
            }
        }
    }

    async fn shutdown_signal() {
        let ctrl_c = async {
            let _ = tokio::signal::ctrl_c().await;
        };
        #[cfg(unix)]
        let terminate = async {
            match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
                Ok(mut signal) => {
                    signal.recv().await;
                }
                Err(_) => std::future::pending::<()>().await,
            }
        };
        #[cfg(not(unix))]
        let terminate = std::future::pending::<()>();
        tokio::select! {
            () = ctrl_c => {},
            () = terminate => {},
        }
    }
}

#[cfg(all(feature = "store-postgres", feature = "embedding-local"))]
#[tokio::main]
async fn main() -> std::process::ExitCode {
    run::run().await
}
