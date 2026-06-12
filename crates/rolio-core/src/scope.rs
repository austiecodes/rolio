use std::{fmt, str::FromStr};

use crate::Error;

/// The scope in which a memory is stored.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum Scope {
    Global,
    Project(ProjectKey),
}

/// An explicit project key. Its text is preserved without normalization.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct ProjectKey(String);

impl ProjectKey {
    pub fn new(value: impl Into<String>) -> Result<Self, Error> {
        let value = value.into();
        if value.is_empty() {
            return Err(Error::EmptyProjectKey);
        }
        if value.chars().any(char::is_control) {
            return Err(Error::ProjectKeyControlCharacter);
        }
        if value.trim() != value {
            return Err(Error::ProjectKeyWhitespace);
        }
        Ok(Self(value))
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }

    pub fn into_string(self) -> String {
        self.0
    }
}

impl FromStr for ProjectKey {
    type Err = Error;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        Self::new(value)
    }
}

impl fmt::Display for ProjectKey {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        self.0.fmt(formatter)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accepts_and_preserves_valid_keys() {
        for value in [
            "a",
            "Rolio",
            "team/project",
            "project:key",
            "project with spaces",
            "caf\u{e9}",
            "alpha\u{a0}beta",
            "git@example.com:team/project.git",
        ] {
            let key = ProjectKey::new(value).unwrap();
            assert_eq!(key.as_str(), value);
            assert_eq!(key.to_string(), value);
            assert_eq!(key.into_string(), value);
        }
    }

    #[test]
    fn accepts_owned_strings() {
        let key = ProjectKey::new(String::from("project")).unwrap();
        assert_eq!(key.as_str(), "project");
    }

    #[test]
    fn rejects_empty_keys() {
        assert!(matches!(ProjectKey::new(""), Err(Error::EmptyProjectKey)));
    }

    #[test]
    fn rejects_boundary_whitespace_without_trimming() {
        for value in [
            " ",
            "  ",
            " project",
            "project ",
            " project ",
            "\u{a0}project",
            "project\u{2003}",
            "\u{a0}\u{2003}",
        ] {
            assert!(matches!(
                ProjectKey::new(value),
                Err(Error::ProjectKeyWhitespace)
            ));
        }
    }

    #[test]
    fn rejects_all_control_characters() {
        for code in (0..=0x1f).chain(0x7f..=0x9f) {
            let character = char::from_u32(code).unwrap();
            let value = format!("pro{character}ject");
            assert!(matches!(
                ProjectKey::new(value),
                Err(Error::ProjectKeyControlCharacter)
            ));
        }
    }

    #[test]
    fn rejects_control_characters_at_boundaries() {
        for value in ["\0project", "project\0", "\tproject", "project\n", "\r\n"] {
            assert!(matches!(
                ProjectKey::new(value),
                Err(Error::ProjectKeyControlCharacter)
            ));
        }
    }

    #[test]
    fn parsing_uses_the_same_validation() {
        assert_eq!(
            "project".parse::<ProjectKey>().unwrap(),
            ProjectKey::new("project").unwrap()
        );
        assert!(matches!(
            "".parse::<ProjectKey>(),
            Err(Error::EmptyProjectKey)
        ));
        assert!(matches!(
            " project".parse::<ProjectKey>(),
            Err(Error::ProjectKeyWhitespace)
        ));
        assert!(matches!(
            "pro\nject".parse::<ProjectKey>(),
            Err(Error::ProjectKeyControlCharacter)
        ));
    }

    #[test]
    fn does_not_impose_a_key_length_limit() {
        let value = "a".repeat(100_000);
        assert_eq!(ProjectKey::new(value.clone()).unwrap().as_str(), value);
    }
}
