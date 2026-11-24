#define _CRT_SECURE_NO_WARNINGS
#include <stdio.h>
#include <string.h>
#include <windows.h>
#include "curve25519.h"
#include "crypto_cng.h"

static int failures = 0;

static void check(int ok, const char *label) {
    fprintf(stderr, "%s %s\n", ok ? "ok  " : "FAIL", label);
    if (!ok) failures++;
}

static void write_all(const char *path, const char *text) {
    HANDLE h = CreateFileA(path, GENERIC_WRITE, 0, NULL, CREATE_ALWAYS,
                           FILE_ATTRIBUTE_NORMAL, NULL);
    DWORD written = 0;
    if (h == INVALID_HANDLE_VALUE) { check(0, "selftest temp file create"); return; }
    if (*text) WriteFile(h, text, (DWORD)strlen(text), &written, NULL);
    CloseHandle(h);
}

static int read_all(const char *path, char *buf, DWORD cap, DWORD *len) {
    HANDLE h = CreateFileA(path, GENERIC_READ, FILE_SHARE_READ, NULL,
                           OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL);
    DWORD rd = 0;
    if (h == INVALID_HANDLE_VALUE) return -1;
    if (!ReadFile(h, buf, cap - 1, &rd, NULL)) { CloseHandle(h); return -1; }
    CloseHandle(h);
    buf[rd] = '\0';
    *len = rd;
    return 0;
}

static void selftest_cas(void) {
    char path[MAX_PATH] = {0};
    char err[256] = {0};
    char buf[256] = {0};
    char pat[MAX_PATH] = {0};
    DWORD len = 0;
    BYTE baseline[32], expected[32], newsha[32], empty[32], wrong[32];
    long long mtime = 0;
    WIN32_FIND_DATAA fd;
    HANDLE fh;
    const char *base = "baseline line\n";
    const char *updated = "updated line\n";

    if (!GetTempFileNameA(".", "fc2", 0, path)) {
        check(0, "selftest temp path");
        return;
    }
    write_all(path, base);
    check(cng_sha256_file(path, baseline) == 0, "streaming hash of existing file");
    check(cng_sha256((const BYTE *)base, (DWORD)strlen(base), expected) == 0 &&
          memcmp(baseline, expected, 32) == 0, "streaming hash matches memory hash");

    check(cng_cas_write_file(path, (const BYTE *)updated, (DWORD)strlen(updated),
                             baseline, newsha, &mtime, err, sizeof(err)) == 0,
          "CAS write nonempty file");
    check(read_all(path, buf, sizeof(buf), &len) == 0 && len == strlen(updated) &&
          memcmp(buf, updated, len) == 0, "CAS write replaced content");
    check(cng_sha256_file(path, expected) == 0 && memcmp(expected, newsha, 32) == 0,
          "CAS write reports new SHA-256");
    check(mtime > 0, "CAS write reports mtime");

    check(cng_cas_write_file(path, NULL, 0, newsha, expected, &mtime,
                             err, sizeof(err)) == 0, "CAS write empty file");
    check(cng_sha256(NULL, 0, empty) == 0 && memcmp(expected, empty, 32) == 0 &&
          read_all(path, buf, sizeof(buf), &len) == 0 && len == 0,
          "empty file hashes to SHA256(\"\")");

    memset(wrong, 0, 32);
    check(cng_cas_write_file(path, (const BYTE *)"stale", 5, newsha, expected,
                             &mtime, err, sizeof(err)) == 1, "stale baseline conflicts");
    check(cng_cas_write_file(path, (const BYTE *)"wrong", 5, wrong, expected,
                             &mtime, err, sizeof(err)) == 1, "wrong baseline conflicts");
    check(read_all(path, buf, sizeof(buf), &len) == 0 && len == 0,
          "conflict did not write");

    _snprintf(pat, sizeof(pat), "%s.fc2tmp.*", path);
    fh = FindFirstFileA(pat, &fd);
    check(fh == INVALID_HANDLE_VALUE, "conflict removed temp file");
    if (fh != INVALID_HANDLE_VALUE) FindClose(fh);
    DeleteFileA(path);
}

int main(void) {
    DWORD len = 0;
    BYTE *d;
    fprintf(stderr, "A\n");
    d = b64dec("n88jGZSgdLfgpQS/C0PL4uRC+gcsnWeR+wwOq6HEX+I=", &len);
    fprintf(stderr, "B d=%p len=%lu\n", (void *)d, (unsigned long)len);
    check(x25519_selftest() == 0, "x25519 self-test");
    check(d != NULL && len == 32, "base64 decode");
    free(d);
    selftest_cas();
    printf("selftest=%d\n", failures);
    fflush(stdout);
    return failures ? 1 : 0;
}
