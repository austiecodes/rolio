use std::{fmt, str::FromStr};

use uuid::Uuid;

use crate::Error;

/// A stable UUID with no business meaning.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct MemoryId(Uuid);

impl MemoryId {
    /// Generate a new random UUID.
    pub fn new() -> Self {
        Self(Uuid::new_v4())
    }
}

impl Default for MemoryId {
    fn default() -> Self {
        Self::new()
    }
}

impl FromStr for MemoryId {
    type Err = Error;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        Uuid::parse_str(value).map(Self).map_err(Error::from)
    }
}

impl fmt::Display for MemoryId {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        self.0.fmt(formatter)
    }
}

#[cfg(test)]
mod tests {
    use std::collections::{BTreeSet, HashSet};

    use super::*;

    #[test]
    fn new_and_default_generate_random_version_four_ids() {
        let first = MemoryId::new();
        let second = MemoryId::default();
        assert_ne!(first, second);
        for id in [first, second] {
            let uuid = Uuid::parse_str(&id.to_string()).unwrap();
            assert_eq!(uuid.get_version_num(), 4);
            assert!(!uuid.is_nil());
        }
    }

    #[test]
    fn parses_and_displays_a_canonical_uuid() {
        let value = "01234567-89ab-4def-8123-456789abcdef";
        let id: MemoryId = value.parse().unwrap();
        assert_eq!(id.to_string(), value);
        assert_eq!(id.to_string().parse::<MemoryId>().unwrap(), id);
    }

    #[test]
    fn accepts_supported_uuid_forms() {
        let expected: MemoryId = "01234567-89ab-4def-8123-456789abcdef".parse().unwrap();
        for value in [
            "01234567-89AB-4DEF-8123-456789ABCDEF",
            "0123456789ab4def8123456789abcdef",
            "{01234567-89ab-4def-8123-456789abcdef}",
            "urn:uuid:01234567-89ab-4def-8123-456789abcdef",
        ] {
            assert_eq!(value.parse::<MemoryId>().unwrap(), expected);
        }
    }

    #[test]
    fn does_not_restrict_parsed_uuid_versions() {
        for value in [
            "00000000-0000-0000-0000-000000000000",
            "01234567-89ab-7def-8123-456789abcdef",
        ] {
            assert_eq!(value.parse::<MemoryId>().unwrap().to_string(), value);
        }
    }

    #[test]
    fn rejects_invalid_uuid_text() {
        for value in [
            "",
            "not-a-uuid",
            "01234567-89ab-4def-8123-456789abcde",
            "01234567-89ab-4def-8123-456789abcdef0",
            "01234567-89ab-4def-8123-456789abcdeg",
            " 01234567-89ab-4def-8123-456789abcdef",
            "01234567-89ab-4def-8123-456789abcdef ",
        ] {
            let error = value.parse::<MemoryId>().unwrap_err();
            assert!(matches!(error, Error::InvalidMemoryId(_)));
            assert!(std::error::Error::source(&error).is_some());
        }
    }

    #[test]
    fn identity_supports_copy_hash_and_order() {
        let first: MemoryId = "00000000-0000-0000-0000-000000000001".parse().unwrap();
        let second: MemoryId = "00000000-0000-0000-0000-000000000002".parse().unwrap();
        let copy = first;
        assert_eq!(copy, first);
        assert_eq!(HashSet::from([first, copy, second]).len(), 2);
        let ordered = BTreeSet::from([second, first]);
        assert_eq!(ordered.into_iter().collect::<Vec<_>>(), [first, second]);
    }
}
