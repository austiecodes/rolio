//! CLI configuration. Resolution order is fixed: explicit flag, environment
//! variable, user configuration file, default. A project repository can never
//! override the server URL or the token source.

use std::{path::PathBuf, time::Duration};

use serde::Deserialize;

pub const DEFAULT_URL: &str = "http://127.0.0.1:8080";
pub const DEFAULT_TIMEOUT_SECS: u64 = 30;

#[derive(Deserialize, Default, Debug)]
struct UserFile {
    url: Option<String>,
    token: Option<String>,
    timeout_secs: Option<u64>,
}

#[derive(Clone, Debug)]
pub struct Config {
    pub url: String,
    pub token: String,
    pub timeout: Duration,
}

#[derive(Debug, thiserror::Error)]
pub enum ConfigError {
    #[error("no token: set --token, ROLO_TOKEN, or token in {}", user_config_path().display())]
    MissingToken,
}

pub fn user_config_path() -> PathBuf {
    if let Some(dir) = std::env::var("XDG_CONFIG_HOME")
        .ok()
        .map(|dir| dir.trim().to_owned())
        .filter(|dir| !dir.is_empty())
    {
        return PathBuf::from(dir).join("rolio/config.toml");
    }
    let home = std::env::var("HOME").unwrap_or_default();
    PathBuf::from(home).join(".config/rolio/config.toml")
}

/// `flag` wins over the environment; the environment wins over the user file.
pub fn resolve(
    flag_url: Option<&str>,
    flag_token: Option<&str>,
    flag_timeout: Option<u64>,
) -> Result<Config, ConfigError> {
    let env = |name: &str| std::env::var(name).ok().filter(|value| !value.is_empty());
    resolve_with(
        flag_url,
        flag_token,
        flag_timeout,
        env("ROLO_URL"),
        env("ROLO_TOKEN"),
        env("ROLO_TIMEOUT").and_then(|value| value.parse().ok()),
        read_user_file(),
    )
}

fn resolve_with(
    flag_url: Option<&str>,
    flag_token: Option<&str>,
    flag_timeout: Option<u64>,
    env_url: Option<String>,
    env_token: Option<String>,
    env_timeout: Option<u64>,
    file: Option<UserFile>,
) -> Result<Config, ConfigError> {
    let url = flag_url
        .map(str::to_owned)
        .or(env_url)
        .or_else(|| file.as_ref().and_then(|file| file.url.clone()))
        .unwrap_or_else(|| DEFAULT_URL.to_owned());
    let token = flag_token
        .map(str::to_owned)
        .or(env_token)
        .or_else(|| file.as_ref().and_then(|file| file.token.clone()))
        .ok_or(ConfigError::MissingToken)?;
    let timeout_secs = flag_timeout
        .or(env_timeout)
        .or_else(|| file.as_ref().and_then(|file| file.timeout_secs))
        .unwrap_or(DEFAULT_TIMEOUT_SECS)
        .max(1);
    Ok(Config {
        url,
        token,
        timeout: Duration::from_secs(timeout_secs),
    })
}

fn read_user_file() -> Option<UserFile> {
    let text = std::fs::read_to_string(user_config_path()).ok()?;
    toml::from_str(&text).ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn resolve_pure(
        flag_url: Option<&str>,
        flag_token: Option<&str>,
        flag_timeout: Option<u64>,
        env_url: Option<&str>,
        env_token: Option<&str>,
        file: Option<UserFile>,
    ) -> Result<Config, ConfigError> {
        resolve_with(
            flag_url,
            flag_token,
            flag_timeout,
            env_url.map(str::to_owned),
            env_token.map(str::to_owned),
            None,
            file,
        )
    }

    #[test]
    fn flags_beat_environment_and_file() {
        let config = resolve_pure(
            Some("http://flag.example"),
            Some("flag-token"),
            Some(7),
            Some("http://env.example"),
            Some("env-token"),
            Some(UserFile {
                url: Some(String::from("http://file.example")),
                token: Some(String::from("file-token")),
                timeout_secs: Some(9),
            }),
        )
        .unwrap();
        assert_eq!(config.url, "http://flag.example");
        assert_eq!(config.token, "flag-token");
        assert_eq!(config.timeout, Duration::from_secs(7));
    }

    #[test]
    fn environment_beats_file_and_default() {
        let config = resolve_pure(
            None,
            None,
            None,
            Some("http://env.example"),
            Some("env-token"),
            Some(UserFile {
                url: Some(String::from("http://file.example")),
                token: Some(String::from("file-token")),
                timeout_secs: Some(9),
            }),
        )
        .unwrap();
        assert_eq!(config.url, "http://env.example");
        assert_eq!(config.token, "env-token");
        // No environment timeout here, so the file value applies.
        assert_eq!(config.timeout, Duration::from_secs(9));
    }

    #[test]
    fn file_beats_defaults() {
        let config = resolve_pure(
            None,
            None,
            None,
            None,
            None,
            Some(UserFile {
                url: Some(String::from("http://file.example")),
                token: Some(String::from("file-token")),
                timeout_secs: Some(9),
            }),
        )
        .unwrap();
        assert_eq!(config.url, "http://file.example");
        assert_eq!(config.token, "file-token");
        assert_eq!(config.timeout, Duration::from_secs(9));
    }

    #[test]
    fn url_defaults_apply_but_a_token_is_mandatory() {
        assert!(matches!(
            resolve_pure(None, None, None, None, None, None).err(),
            Some(ConfigError::MissingToken)
        ));
        assert_eq!(DEFAULT_URL, "http://127.0.0.1:8080");
    }

    #[test]
    fn missing_token_is_an_explicit_error() {
        assert!(matches!(
            resolve_pure(None, None, None, None, None, None).err(),
            Some(ConfigError::MissingToken)
        ));
    }

    #[test]
    fn timeout_has_a_floor_of_one_second() {
        let config = resolve_pure(Some("http://x"), Some("t"), Some(0), None, None, None).unwrap();
        assert_eq!(config.timeout, Duration::from_secs(1));
    }
}
