use std::{
    fs,
    io::Read,
    path::{Path, PathBuf},
};

use serde::Deserialize;
use sha2::{Digest, Sha256};

use super::EmbeddingError;

pub(super) const MANIFEST: &str = include_str!("artifacts.json");

#[derive(Deserialize)]
pub(super) struct Manifest {
    pub model: String,
    pub revision: String,
    pub files: Vec<Artifact>,
}

#[derive(Deserialize)]
pub(super) struct Artifact {
    pub name: String,
    pub sha256: String,
}

pub(super) fn verify(directory: &Path) -> Result<Manifest, EmbeddingError> {
    let manifest: Manifest =
        serde_json::from_str(MANIFEST).map_err(|_| EmbeddingError::IncompatibleProfile)?;
    for artifact in &manifest.files {
        verify_file(directory, artifact)?;
    }
    Ok(manifest)
}

fn verify_file(directory: &Path, artifact: &Artifact) -> Result<PathBuf, EmbeddingError> {
    let name = Path::new(&artifact.name);
    if name.file_name().is_none_or(|file| file != name.as_os_str()) {
        return Err(EmbeddingError::IncompatibleProfile);
    }
    let path = directory.join(name);
    let metadata = fs::symlink_metadata(&path).map_err(|_| EmbeddingError::Unavailable)?;
    if !metadata.is_file() || metadata.len() > 1024 * 1024 * 1024 {
        return Err(EmbeddingError::IncompatibleProfile);
    }
    let mut file = fs::File::open(&path).map_err(|_| EmbeddingError::Unavailable)?;
    let mut hash = Sha256::new();
    let mut buffer = [0; 64 * 1024];
    loop {
        let size = file
            .read(&mut buffer)
            .map_err(|_| EmbeddingError::Unavailable)?;
        if size == 0 {
            break;
        }
        hash.update(&buffer[..size]);
    }
    if format!("{:x}", hash.finalize()) != artifact.sha256 {
        return Err(EmbeddingError::IncompatibleProfile);
    }
    Ok(path)
}

pub(super) fn digest() -> [u8; 32] {
    Sha256::digest(MANIFEST.as_bytes()).into()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_changed_missing_and_nonlocal_artifacts() {
        let dir = tempfile::tempdir().unwrap();
        let artifact = Artifact {
            name: "model.onnx".into(),
            sha256: format!("{:x}", Sha256::digest(b"fixture")),
        };
        assert_eq!(
            verify_file(dir.path(), &artifact),
            Err(EmbeddingError::Unavailable)
        );
        fs::write(dir.path().join(&artifact.name), b"fixture").unwrap();
        assert!(verify_file(dir.path(), &artifact).is_ok());
        fs::write(dir.path().join(&artifact.name), b"changed").unwrap();
        assert_eq!(
            verify_file(dir.path(), &artifact),
            Err(EmbeddingError::IncompatibleProfile)
        );
        for name in ["../model.onnx", "/model.onnx", "", ".", ".."] {
            let artifact = Artifact {
                name: name.into(),
                sha256: String::new(),
            };
            assert_eq!(
                verify_file(dir.path(), &artifact),
                Err(EmbeddingError::IncompatibleProfile)
            );
        }
    }
}
