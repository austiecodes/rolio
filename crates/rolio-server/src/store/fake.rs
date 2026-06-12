//! Test-only reference adapter. It provides no durability evidence.

use std::collections::BTreeMap;

use async_trait::async_trait;
use rolio_core::MemoryId;
use tokio::sync::Mutex;

use super::*;

pub(crate) struct FakeStore {
    identity: StoreIdentity,
    records: Mutex<BTreeMap<MemoryId, StoredMemory>>,
}

impl FakeStore {
    pub(crate) fn new(identity: StoreIdentity) -> Self {
        Self {
            identity,
            records: Mutex::new(BTreeMap::new()),
        }
    }
}

#[async_trait]
impl MemoryStore for FakeStore {
    async fn get(&self, id: MemoryId) -> Result<Option<StoredMemory>, StoreError> {
        Ok(self.records.lock().await.get(&id).cloned())
    }

    async fn write(&self, request: WriteRequest) -> Result<StoredMemory, StoreError> {
        request
            .memory
            .validate()
            .map_err(|_| StoreError::InvalidMemory)?;
        self.identity
            .embedding_profile
            .validate_vector(&request.vector)?;
        let mut records = self.records.lock().await;
        let previous = records.get(&request.memory.id);
        match request.condition {
            WriteCondition::Absent if previous.is_some() => return Err(StoreError::AlreadyExists),
            WriteCondition::Revision(expected)
                if previous.is_none_or(|record| record.revision != expected) =>
            {
                return Err(StoreError::RevisionConflict);
            }
            _ => {}
        }
        if previous.is_some_and(|record| record.memory.scope != request.memory.scope) {
            return Err(StoreError::ScopeChange);
        }
        let record = StoredMemory {
            memory: request.memory,
            revision: Revision::new(),
            vector: request.vector,
        };
        records.insert(record.memory.id, record.clone());
        Ok(record)
    }

    async fn delete(&self, id: MemoryId, revision: Revision) -> Result<(), StoreError> {
        let mut records = self.records.lock().await;
        if records
            .get(&id)
            .is_none_or(|record| record.revision != revision)
        {
            return Err(StoreError::RevisionConflict);
        }
        records.remove(&id);
        Ok(())
    }

    async fn list(&self, request: ListRequest) -> Result<ListPage, StoreError> {
        request.validate()?;
        let records = self.records.lock().await;
        let mut matches = records.values().filter(|record| {
            record.memory.scope == request.scope
                && request
                    .cursor
                    .as_ref()
                    .is_none_or(|cursor| record.memory.id > cursor.after)
        });
        let page: Vec<_> = matches
            .by_ref()
            .take(request.limit.get())
            .cloned()
            .collect();
        let next = if matches.next().is_some() {
            page.last().map(|record| ListCursor {
                scope: request.scope,
                after: record.memory.id,
            })
        } else {
            None
        };
        Ok(ListPage {
            records: page,
            next,
        })
    }

    async fn search(&self, request: SearchRequest) -> Result<Vec<SearchHit>, StoreError> {
        request.validate()?;
        self.identity
            .embedding_profile
            .validate_vector(&request.vector)?;
        let records = self.records.lock().await;
        let mut hits: Vec<_> = records
            .values()
            .filter(|record| request.scopes.contains(&record.memory.scope))
            .map(|record| SearchHit {
                record: record.clone(),
                score: cosine(request.vector.as_slice(), record.vector.as_slice()),
            })
            .collect();
        hits.sort_by(|left, right| {
            right
                .score
                .total_cmp(&left.score)
                .then_with(|| left.record.memory.id.cmp(&right.record.memory.id))
        });
        hits.truncate(request.limit.get());
        Ok(hits)
    }

    async fn check(&self, expected: &StoreIdentity) -> Result<(), StoreError> {
        if expected != &self.identity {
            return Err(StoreError::IncompatibleIdentity);
        }
        Ok(())
    }
}

fn cosine(left: &[f32], right: &[f32]) -> f32 {
    let dot: f64 = left
        .iter()
        .zip(right)
        .map(|(&a, &b)| f64::from(a) * f64::from(b))
        .sum();
    let norm = |values: &[f32]| {
        values
            .iter()
            .map(|&v| f64::from(v).powi(2))
            .sum::<f64>()
            .sqrt()
    };
    (dot / (norm(left) * norm(right))).clamp(-1.0, 1.0) as f32
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::store::contracts;

    #[tokio::test]
    async fn common_storage_contract() {
        contracts::run(&FakeStore::new(contracts::identity())).await;
    }
}
