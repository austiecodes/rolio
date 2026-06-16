use std::sync::{Arc, Mutex, mpsc as sync_mpsc};

use super::*;
use crate::store::contracts;

struct FixedModel;

impl BlockingModel for FixedModel {
    fn infer(&mut self, _: &str, _: InputKind) -> Result<Vector, EmbeddingError> {
        Ok(Vector::new(vec![1.0, 0.0, 0.0]).unwrap())
    }
}

#[tokio::test]
async fn validates_inputs_and_dimensions() {
    let mut profile = contracts::identity().embedding_profile;
    profile.dimensions = 3.try_into().unwrap();
    let worker = LocalEmbedding::start(
        || Ok(FixedModel),
        profile.clone(),
        1,
        Duration::from_secs(2),
    )
    .unwrap();
    let result = worker.embed("text", InputKind::Document).await.unwrap();
    assert_eq!(result.profile, profile);
    for text in [
        String::new(),
        " \n\t".into(),
        "a".repeat(LocalEmbedding::MAX_INPUT_BYTES + 1),
    ] {
        assert_eq!(
            worker.embed(&text, InputKind::Query).await.unwrap_err(),
            EmbeddingError::InvalidInput
        );
    }
    profile.dimensions = 2.try_into().unwrap();
    let worker =
        LocalEmbedding::start(|| Ok(FixedModel), profile, 1, Duration::from_secs(2)).unwrap();
    assert_eq!(
        worker.embed("text", InputKind::Query).await.unwrap_err(),
        EmbeddingError::InvalidVector
    );
}

#[test]
fn rejects_unbounded_worker_settings() {
    for (capacity, timeout) in [(0, 1), (33, 1), (1, 0), (1, 301)] {
        assert!(
            LocalEmbedding::start(
                || Ok(FixedModel),
                contracts::identity().embedding_profile,
                capacity,
                Duration::from_secs(timeout)
            )
            .is_err()
        );
    }
}

struct BlockingFirst {
    entered: Option<oneshot::Sender<()>>,
    release: sync_mpsc::Receiver<()>,
    calls: Arc<Mutex<Vec<String>>>,
}

impl BlockingModel for BlockingFirst {
    fn infer(&mut self, text: &str, _: InputKind) -> Result<Vector, EmbeddingError> {
        self.calls.lock().unwrap().push(text.into());
        if let Some(entered) = self.entered.take() {
            let _ = entered.send(());
            self.release.recv_timeout(Duration::from_secs(5)).unwrap();
        }
        FixedModel.infer(text, InputKind::Document)
    }
}

#[tokio::test]
async fn queue_is_bounded_and_cancelled_jobs_are_skipped() {
    let (entered, started) = oneshot::channel();
    let (release, blocked) = sync_mpsc::channel();
    let calls = Arc::new(Mutex::new(Vec::new()));
    let mut profile = contracts::identity().embedding_profile;
    profile.dimensions = 3.try_into().unwrap();
    let model_entered = Some(entered);
    let model_calls = calls.clone();
    let worker = LocalEmbedding::start(
        move || {
            Ok(BlockingFirst {
                entered: model_entered,
                release: blocked,
                calls: model_calls,
            })
        },
        profile,
        1,
        Duration::from_secs(2),
    )
    .unwrap();

    let mut first = Box::pin(worker.embed("active", InputKind::Document));
    tokio::select! {
        result = &mut first => panic!("inference finished early: {result:?}"),
        result = tokio::time::timeout(Duration::from_secs(2), started) => result.unwrap().unwrap(),
    }
    // Direct queue insertion makes the cancellation/full-queue schedule exact.
    let (reply, receiver) = oneshot::channel();
    worker
        .sender
        .try_send(Job {
            text: "cancelled".into(),
            kind: InputKind::Document,
            reply,
        })
        .unwrap();
    assert_eq!(
        worker
            .embed("overflow", InputKind::Query)
            .await
            .unwrap_err(),
        EmbeddingError::Busy
    );
    drop(receiver);
    release.send(()).unwrap();
    first.await.unwrap();
    // A sentinel reply proves that the worker has passed the cancelled job.
    let (reply, receiver) = oneshot::channel();
    worker
        .sender
        .send(Job {
            text: "sentinel".into(),
            kind: InputKind::Query,
            reply,
        })
        .await
        .unwrap();
    tokio::time::timeout(Duration::from_secs(2), receiver)
        .await
        .unwrap()
        .unwrap()
        .unwrap();
    assert_eq!(*calls.lock().unwrap(), ["active", "sentinel"]);
}

#[tokio::test]
async fn active_timeout_does_not_start_another_inference_in_parallel() {
    let (entered, started) = oneshot::channel();
    let (release, blocked) = sync_mpsc::channel();
    let calls = Arc::new(Mutex::new(Vec::new()));
    let mut profile = contracts::identity().embedding_profile;
    profile.dimensions = 3.try_into().unwrap();
    let model_entered = Some(entered);
    let model_calls = calls.clone();
    let worker = LocalEmbedding::start(
        move || {
            Ok(BlockingFirst {
                entered: model_entered,
                release: blocked,
                calls: model_calls,
            })
        },
        profile,
        1,
        Duration::from_millis(100),
    )
    .unwrap();
    let mut first = Box::pin(worker.embed("active", InputKind::Document));
    tokio::select! {
        result = &mut first => panic!("inference finished early: {result:?}"),
        result = tokio::time::timeout(Duration::from_secs(2), started) => result.unwrap().unwrap(),
    }
    assert_eq!(first.await.unwrap_err(), EmbeddingError::Timeout);
    assert_eq!(
        worker.embed("queued", InputKind::Query).await.unwrap_err(),
        EmbeddingError::Timeout
    );
    assert_eq!(*calls.lock().unwrap(), ["active"]);
    release.send(()).unwrap();
    let (reply, receiver) = oneshot::channel();
    worker
        .sender
        .send(Job {
            text: "sentinel".into(),
            kind: InputKind::Query,
            reply,
        })
        .await
        .unwrap();
    tokio::time::timeout(Duration::from_secs(2), receiver)
        .await
        .unwrap()
        .unwrap()
        .unwrap();
    assert_eq!(*calls.lock().unwrap(), ["active", "sentinel"]);
}
