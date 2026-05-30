# PH08 - Production Flow Refactor

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Goal: split production start and confirmation logic while preserving production invariants.

### Checklist

- [x] Split memory production start validation from mutation.
- [x] Split PostgreSQL production start validation from mutation.
- [x] Split memory production confirmation validation from mutation.
- [x] Split PostgreSQL production confirmation validation from mutation.
- [x] Extract allocation lookup helpers.
- [x] Extract full-completion mutation helper.
- [x] Extract partial-completion mutation helper.
- [x] Extract remainder allocation creation helper.
- [x] Extract production audit reason formatting.
- [x] Confirm starting production locks scheduled allocations.
- [x] Confirm over-quantity production is rejected.
- [x] Confirm partial production keeps completed allocation history.
- [x] Confirm partial production returns the remainder to the pending queue.
- [x] Confirm line revision bumps after production state changes.
- [x] Run production start tests.
- [x] Run full confirm tests.
- [x] Run partial confirm tests.
- [x] Run over-quantity rejection tests.
- [x] Update `doc/progress.md` when this phase is complete.

## 繁體中文

目標：拆分 production start 與 confirmation logic，同時保留 production invariant。

### Checklist

- [x] 將 memory production start validation 與 mutation 拆開。
- [x] 將 PostgreSQL production start validation 與 mutation 拆開。
- [x] 將 memory production confirmation validation 與 mutation 拆開。
- [x] 將 PostgreSQL production confirmation validation 與 mutation 拆開。
- [x] 抽出 allocation lookup helpers。
- [x] 抽出 full-completion mutation helper。
- [x] 抽出 partial-completion mutation helper。
- [x] 抽出 remainder allocation creation helper。
- [x] 抽出 production audit reason formatting。
- [x] 確認 starting production 會 lock scheduled allocations。
- [x] 確認 over-quantity production 會被拒絕。
- [x] 確認 partial production 保留 completed allocation history。
- [x] 確認 partial production 會將 remainder 回到 pending queue。
- [x] 確認 production state change 後 line revision 會 bump。
- [x] 執行 production start tests。
- [x] 執行 full confirm tests。
- [x] 執行 partial confirm tests。
- [x] 執行 over-quantity rejection tests。
- [x] 完成本階段後更新 `doc/progress.md`。
