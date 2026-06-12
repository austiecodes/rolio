/// An error in a memory domain value.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("invalid memory ID: {0}")]
    InvalidMemoryId(#[from] uuid::Error),

    #[error("project key must not be empty")]
    EmptyProjectKey,

    #[error("project key must not start or end with whitespace")]
    ProjectKeyWhitespace,

    #[error("project key must not contain control characters")]
    ProjectKeyControlCharacter,

    #[error("confidence must be between 0.0 and 1.0 in steps of 0.1")]
    InvalidConfidence,

    #[error("memory content must not be empty or contain only whitespace")]
    EmptyContent,

    #[error("memory update time must not be before its creation time")]
    InvalidTimestampOrder,
}
