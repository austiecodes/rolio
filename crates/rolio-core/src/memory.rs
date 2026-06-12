use crate::{Confidence, Error, MemoryId, Scope};

pub type Timestamp = time::OffsetDateTime;

/// The controlled category of a memory.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum MemoryKind {
    Note,
    Preference,
    Convention,
    Fact,
    Solution,
    Lesson,
    Plan,
}

/// Optional source details, preserved without normalization.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Source {
    pub uri: Option<String>,
    pub agent: Option<String>,
    pub external_reference: Option<String>,
}

/// Memory content and metadata, without storage or write-control fields.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Memory {
    pub id: MemoryId,
    pub scope: Scope,
    pub kind: MemoryKind,
    pub content: String,
    pub confidence: Confidence,
    pub source: Option<Source>,
    pub created_at: Timestamp,
    pub updated_at: Timestamp,
}

impl Memory {
    /// Check content and timestamp order without changing any fields.
    pub fn validate(&self) -> Result<(), Error> {
        if self.content.trim().is_empty() {
            return Err(Error::EmptyContent);
        }
        if self.updated_at < self.created_at {
            return Err(Error::InvalidTimestampOrder);
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use time::{Duration, UtcOffset};

    use crate::ProjectKey;

    use super::*;

    fn memory() -> Memory {
        Memory {
            id: MemoryId::new(),
            scope: Scope::Global,
            kind: MemoryKind::Note,
            content: String::from("Use explicit configuration."),
            confidence: Confidence::default(),
            source: None,
            created_at: Timestamp::UNIX_EPOCH,
            updated_at: Timestamp::UNIX_EPOCH,
        }
    }

    #[test]
    fn accepts_equal_timestamps() {
        assert!(memory().validate().is_ok());
    }

    #[test]
    fn accepts_a_later_update_time() {
        let mut memory = memory();
        memory.updated_at += Duration::nanoseconds(1);
        assert!(memory.validate().is_ok());
    }

    #[test]
    fn rejects_an_earlier_update_time() {
        let mut memory = memory();
        memory.updated_at -= Duration::nanoseconds(1);
        assert!(matches!(
            memory.validate(),
            Err(Error::InvalidTimestampOrder)
        ));
    }

    #[test]
    fn compares_instants_instead_of_local_clock_times() {
        let mut memory = memory();
        memory.created_at = Timestamp::UNIX_EPOCH.to_offset(UtcOffset::from_hms(2, 0, 0).unwrap());
        memory.updated_at = Timestamp::UNIX_EPOCH.to_offset(UtcOffset::from_hms(-2, 0, 0).unwrap());
        assert!(memory.validate().is_ok());
        memory.updated_at -= Duration::seconds(1);
        assert!(matches!(
            memory.validate(),
            Err(Error::InvalidTimestampOrder)
        ));
    }

    #[test]
    fn rejects_empty_and_whitespace_only_content() {
        for content in ["", " ", "\t\r\n", "\u{a0}\u{2003}\u{2028}\u{2029}"] {
            let mut memory = memory();
            memory.content = content.into();
            assert!(matches!(memory.validate(), Err(Error::EmptyContent)));
        }
    }

    #[test]
    fn preserves_content_and_its_whitespace() {
        for content in [" body ", "# Heading\n\nContent.\n", "\n\tKeep spacing.\r\n"] {
            let mut memory = memory();
            memory.content = content.into();
            let original = memory.clone();
            assert!(memory.validate().is_ok());
            assert_eq!(memory, original);
        }
    }

    #[test]
    fn does_not_impose_a_content_length_limit() {
        let mut memory = memory();
        memory.content = "a".repeat(1024 * 1024 + 1);
        assert!(memory.validate().is_ok());
    }

    #[test]
    fn accepts_each_kind_and_scope() {
        for scope in [
            Scope::Global,
            Scope::Project(ProjectKey::new("project").unwrap()),
        ] {
            for kind in [
                MemoryKind::Note,
                MemoryKind::Preference,
                MemoryKind::Convention,
                MemoryKind::Fact,
                MemoryKind::Solution,
                MemoryKind::Lesson,
                MemoryKind::Plan,
            ] {
                let mut memory = memory();
                memory.scope = scope.clone();
                memory.kind = kind;
                assert!(memory.validate().is_ok());
            }
        }
    }

    #[test]
    fn accepts_each_confidence_value() {
        for tenths in 0..=10 {
            let mut memory = memory();
            memory.confidence = Confidence::try_from(tenths as f32 / 10.0).unwrap();
            assert!(memory.validate().is_ok());
        }
    }

    #[test]
    fn source_fields_are_independently_optional() {
        for uri in [None, Some(String::from("../notes#example"))] {
            for agent in [None, Some(String::from("agent name"))] {
                for external_reference in [None, Some(String::from(" session:42 "))] {
                    let mut memory = memory();
                    memory.source = Some(Source {
                        uri: uri.clone(),
                        agent: agent.clone(),
                        external_reference,
                    });
                    let original = memory.clone();
                    assert!(memory.validate().is_ok());
                    assert_eq!(memory, original);
                }
            }
        }
    }

    #[test]
    fn does_not_parse_or_normalize_source_fields() {
        let mut memory = memory();
        memory.source = Some(Source {
            uri: Some(String::from(" HTTPS://Example.COM/a/../b?x=1 ")),
            agent: Some(String::new()),
            external_reference: Some(String::from("external\nreference")),
        });
        let original = memory.clone();
        assert!(memory.validate().is_ok());
        assert_eq!(memory, original);
    }

    #[test]
    fn default_source_has_no_fields() {
        assert_eq!(
            Source::default(),
            Source {
                uri: None,
                agent: None,
                external_reference: None,
            }
        );
    }
}
