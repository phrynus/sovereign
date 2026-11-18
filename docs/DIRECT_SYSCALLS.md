# Direct syscalls: parsers, wiring, and failure modes

`internal/payload/agent` carries a portable (byte-slice-only) syscall-resolution stack. It was
originally landed as an *unwired* capability; **Option B from the original decision record was
later chosen and shipped**, so this page now describes what is actually live. Trust the code and
`CHANGELOG.md`, not any older copy of this page that says "deliberately absent".

## What exists

| File | Does | Touches Windows? |
|---|---|---|
| `pe_exports.go` | name → **RVA** from a PE32/PE32+ export table, refusing forwarders and truncation | **no** — byte slices only |
| `syscall_ssn.go` | **RVA → syscall number**, refusing hooked-but-unparsable stubs, zero, and out-of-range immediates | **no** |
| `syscall_resolve.go` | composes both for the five `Nt*` primitives a syscall layer would need | **no** |
| `syscall_policy_windows.go` | resolves the SSN table **once at startup from ntdll on disk** and exposes `useIndirectSyscall` | Windows |
| `syscall_policy_stub.go` | no-op so the policy flag compiles on every GOOS | non-Windows |

All three parsers build and are tested **on every platform**: the failure mode of this technique is
not "it did not parse" but **"it parsed the wrong thing"**, and a wrong syscall number calls a
different function on the target with no visible symptom.

### The unit trap, recorded because it already bit once

`FindExportRVA` returns an **RVA**; `ExtractSSN` reads bytes at a **file offset**. Passing one where
the other is expected does not fail loudly — it scans the wrong region and reports *"no syscall stub
prologue"*, which reads like a missing export. `ResolveNtSyscalls` translates once, through
`FileOffsetForRVA`, and that translation is what the composed test pins.

## What is wired (Option B)

`agent_main.go` parses `SyscallPolicyStr` (or `FORGEC2_SYSCALL_POLICY=1`) into
`syscallPolicyEnabled` and calls `initSyscallPolicy()` at startup. The table resolves once, from
ntdll **on disk** (never from a mapped image an EDR may have patched). If the table is incomplete the
policy refuses indirect syscalls — there is no silent fallback to a possibly-hooked `Nt*` export.
The indirection switch is independent of sleep-masking, so a bad table can be turned off without
losing sleep obfuscation.

The separate switch is deliberate: the two mechanisms share a purpose (reduce detection) but not a
failure mode (a wrong sleep mask corrupts memory; a wrong SSN calls the wrong kernel routine).
`agent_main.go` cannot name the Windows-only `useIndirectSyscall` directly — that symbol lives in a
Windows-tagged file — which is why the clearing is the platform file's job.

## Limits

- There is no hand-written trampoline: indirect calls through a ntdll gadget (`jmp rbx`) are the
  step that changes the emitted binary and are not taken. The wired policy resolves and gates the
  SSN table; it does not rewrite call sites.
- A correct SSN still requires a compatible stub shape; the parser refuses hooked-but-unparsable
  stubs rather than guessing, which means a heavily hooked ntdll can disable the policy (safely).
