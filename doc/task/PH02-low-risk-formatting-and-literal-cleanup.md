# PH02 - Low-Risk Formatting And Literal Cleanup

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Goal: handle low-risk analyzer findings before larger refactors.

### Checklist

- [x] Split the long `Dockerfile.web` `sed` command into backslash-continued lines.
- [x] Verify the `Dockerfile.web` substitutions and target path are unchanged.
- [x] Add a package-level PostgreSQL line revision SQL constant in `internal/api/postgres_store.go`.
- [x] Replace every exact repeated revision SQL literal with the new constant.
- [x] Confirm no transaction boundaries changed.
- [x] Confirm audit behavior is unchanged.
- [x] Replace the startup promise chain in `web/app.js` with top-level `await`.
- [x] Preserve 401 handling, session clearing, workspace rendering, and warning messages.
- [x] Update the `cssEscape` fallback to use `String.raw`.
- [x] Run focused Go or JavaScript checks for the touched files.
- [x] Optionally run `docker build -f Dockerfile.web .` if Docker is available.
- [x] Update `doc/progress.md` when this phase is complete.

## 繁體中文

目標：先處理低風險 analyzer finding，再進入大型重構。

### Checklist

- [x] 將 `Dockerfile.web` 的長 `sed` 指令拆成 backslash continuation。
- [x] 確認 `Dockerfile.web` 的替換內容與目標路徑不變。
- [x] 在 `internal/api/postgres_store.go` 新增 package-level PostgreSQL line revision SQL constant。
- [x] 將完全相同的 revision SQL literal 全部替換為新 constant。
- [x] 確認 transaction boundary 沒有改變。
- [x] 確認 audit 行為沒有改變。
- [x] 將 `web/app.js` startup promise chain 改成 top-level `await`。
- [x] 保留 401 handling、session clearing、workspace rendering 與 warning message。
- [x] 將 `cssEscape` fallback 改成使用 `String.raw`。
- [x] 針對 touched files 執行聚焦的 Go 或 JavaScript 檢查。
- [x] 若 Docker 可用，可執行 `docker build -f Dockerfile.web .`。
- [x] 完成本階段後更新 `doc/progress.md`。
