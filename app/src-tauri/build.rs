fn main() {
    if std::env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("linux") {
        let arch = match std::env::var("CARGO_CFG_TARGET_ARCH").as_deref() {
            Ok("x86_64") => "amd64",
            Ok("aarch64") => "arm64",
            _ => panic!("unsupported Linux desktop architecture"),
        };
        let dir = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        let output = dir.join("target/update-helper/brain-update");
        std::fs::create_dir_all(output.parent().unwrap()).expect("create helper output directory");
        println!("cargo:rerun-if-changed=../../attach/cmd/brain-update");
        println!("cargo:rerun-if-changed=../../attach/go.mod");
        println!("cargo:rerun-if-changed=../../attach/go.sum");
        let status = std::process::Command::new("go")
            .current_dir(dir.join("../../attach"))
            .env("GOOS", "linux")
            .env("GOARCH", arch)
            .env("CGO_ENABLED", "0")
            .args(["build", "-trimpath", "-o"])
            .arg(output)
            .arg("./cmd/brain-update")
            .status()
            .expect("build desktop update helper with Go");
        assert!(status.success(), "desktop update helper build failed");
    }
    tauri_build::build()
}
