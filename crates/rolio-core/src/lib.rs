//! Memory domain types and validation, independent of storage and transport.

mod confidence;
mod error;
mod memory;
mod memory_id;
mod scope;

pub use confidence::Confidence;
pub use error::Error;
pub use memory::{Memory, MemoryKind, Source, Timestamp};
pub use memory_id::MemoryId;
pub use scope::{ProjectKey, Scope};
