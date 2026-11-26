# تدقيق Seccomp وتكافؤ المعمارية (amd64 مقابل arm64)

يوثق هذا المستند القواعد المفروضة فعلياً في كود `cmd/mcp-guard` عبر فلتر seccomp-BPF، ويفصلها فصلاً تاماً عن الفروق العامة والتاريخية لنواة Linux على معمارية AArch64.

---

## الجزء الأول: ما يفرضه الفلتر فعلياً في كود BPF والنواة (Enforced Rules)

يقوم حارس `cmd/mcp-guard` بتثبيت برنامج BPF عبر `unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, ...)` بعد تفعيل `PR_SET_NO_NEW_PRIVS`، ثم يفتح الملف التنفيذي المستهدف عبر `internal/safefs` (رفض الروابط الرمزية في كل مكوّن والملفات غير المنتظمة والقابلة للكتابة من المجموعة/الغير، واشتراط بت التنفيذ) وينفّذه عبر `execveat(fd, "", argv, envp, AT_EMPTY_PATH)` لإزالة نافذة TOCTOU.

الفلتر مبني على سياسة **قائمة حظر موسّعة وسماح افتراضي (Allow-by-default with Hardened Deny-list)**؛ صُممت بحيث لا تكسر خوادم MCP المعتمدة (Go/glibc/Python/Node مثل REA) على نواة Linux (`amd64`/`arm64`)، مع حظر الفئات الخطرة بـ `SECCOMP_RET_ERRNO | EPERM`:

| العنصر المفحوص | رقم الاستدعاء على `amd64` | رقم الاستدعاء على `arm64` | القرار البرمجي في BPF | النتيجة في النواة | الغرض الأمني |
| :--- | :---: | :---: | :---: | :---: | :--- |
| **`seccomp_data.arch`** | `AUDIT_ARCH_X86_64`<br>(`0xc000003e`) | `AUDIT_ARCH_AARCH64`<br>(`0xc00000b7`) | `SECCOMP_RET_KILL_PROCESS` عند عدم التطابق | قتل فوري بإشارة `SIGSYS` | التحقق من المعمارية أولاً؛ منع هجمات حقن استدعاءات من معمارية أخرى (cross-ABI mismatch). |
| **البت 30 (`0x40000000`)** | `0x40000000` | `0x40000000` | `SECCOMP_RET_KILL_PROCESS` عند ضبط البت | قتل فوري بإشارة `SIGSYS` | على amd64 يمنع الالتفاف عبر x32 ABI؛ وعلى arm64 يقتل أي استدعاء يقع خارج النطاق القياسي. |
| **`SYS_SETSID`, `SYS_SETPGID`** | **112, 109** | **157, 154** | `SECCOMP_RET_ERRNO \| EPERM` | فشل الاستدعاء مع `EPERM` | منع خادم MCP من إنشاء جلسة جديدة أو تغيير مجموعة العمليات للانفصال عن شجرة العمليات. |
| **التصحيح وحقن الذاكرة والمقابض** | `ptrace`, `process_vm_readv/writev`, `process_madvise`, `kcmp`, `pidfd_getfd` | نفسها (وفق `zsysnum_linux_arm64`) | `SECCOMP_RET_ERRNO \| EPERM` | فشل الاستدعاء مع `EPERM` | منع فحص أو تعديل ذاكرة/واصفات العمليات الأخرى. |
| **مساحات الأسماء والتركيب (`mount`)** | `unshare`, `setns`, `mount`, `umount2`, `pivot_root`, `chroot`, `open_tree`, `move_mount`, `fsopen`, `fsconfig`, `fsmount`, `fspick`, `mount_setattr`, `open_by_handle_at`, `name_to_handle_at` | نفسها (وفق `zsysnum_linux_arm64`) | `SECCOMP_RET_ERRNO \| EPERM` | فشل الاستدعاء مع `EPERM` | منع الهروب عبر مساحات الأسماء أو تركيب أنظمة ملفات أو فتح مقابض خارج الحاوية. |
| **سطح هجوم النواة والتجاوز** | `bpf`, `io_uring_setup/enter/register`, `userfaultfd`, `perf_event_open` | نفسها (وفق `zsysnum_linux_arm64`) | `SECCOMP_RET_ERRNO \| EPERM` | فشل الاستدعاء مع `EPERM` | سد استدعاءات `io_uring` و`bpf` و`userfaultfd` عالية الخطورة وتجاوز مراقبة الاستدعاءات. |
| **وحدات النواة، المفاتيح، التوقيت، التراثيات** | `init_module`, `finit_module`, `delete_module`, `kexec_*`, `reboot`, `swapon/off`, `add_key`, `request_key`, `keyctl`, `personality`, `syslog`, `quotactl`, `acct`, `vhangup`, `sethostname`, `setdomainname`, `adjtimex`, `clock_adjtime`, `clock_settime`, `settimeofday`, `lookup_dcookie` + (`uselib`, `ustat`, `sysfs`, `modify_ldt`, `iopl`, `ioperm`, `create_module`, `get_kernel_syms`, `query_module` في `arch_amd64.go`) | المشتركة في `filter_linux.go` | `SECCOMP_RET_ERRNO \| EPERM` | فشل الاستدعاء مع `EPERM` | منع الوصول إلى keyring النواة أو تغيير `personality` أو استغلال واجهات النواة التراثية. |
| **بقية استدعاءات النظام (بما فيها `execveat`)** | أي رقم آخر | أي رقم آخر | **`SECCOMP_RET_ALLOW`** | تنفيذ طبيعي دون اعتراض | السماح لخوادم MCP بعملياتها التشغيلية الطبيعية (I/O، ذاكرة، مقابس محلية، خيوط). |

> [!IMPORTANT]
> **تأكيد حاسم**: القاعدة الفعالة هي قائمة حظر موسّعة كما هو موثّق في `commonDeniedSyscalls` (`cmd/mcp-guard/filter_linux.go`) و`archDeniedSyscalls` (`cmd/mcp-guard/arch_amd64.go` / `arch_arm64.go`)؛ وأي استدعاء خارجها يمر إلى `SECCOMP_RET_ALLOW` بينما يبقى عزل الملفات والشبكة مسؤولية `bubblewrap`.

---

## الجزء الثاني: تحليل نظري ومرجعي لمعمارية AArch64 (Theoretical & Ecosystem Context)

هذا القسم هو تحليل مرجعي لمعمارية Linux AArch64 (`arm64`)، لتوضيح كيفية تفاعلها مع النظام وما يضمنه أو لا يضمنه الفلتر:

### 1. الاستدعاءات القديمة الملغاة في معمارية AArch64 (Legacy Syscalls)
عند تصميم معمارية AArch64 في نواة Linux (بدءاً من الإصدار 3.8+ عبر `asm-generic/unistd.h`)، أزالت النواة استدعاءات النظام القديمة واستبدلتها باستدعاءات حديثة قائمة على واصفات المجلدات (`*at`) والـ 64-bit safety:

| استدعاء النظام القديم (الملغى على arm64) | رقم amd64 | البديل المعياري في نواة arm64 | حالة الفلتر تجاه البديل | ملاحظات |
| :--- | :---: | :--- | :---: | :--- |
| `open` | 2 | `openat` (56) و `openat2` (437) | **مسموح (ALLOW)** | الفلتر لا يحظره؛ احتواء المسارات تضمنه بيئة bubblewrap و `os.Root` |
| `stat`, `lstat` | 4, 6 | `newfstatat` (79) | **مسموح (ALLOW)** | الفلتر لا يحظره؛ فحص `Nlink` يتعامل مع `uint32` على arm64 |
| `fork`, `vfork` | 57, 58 | `clone` (220) و `clone3` (435) | **مسموح (ALLOW)** | الفلتر لا يحظرهما؛ تفصيل أدناه حول استحالة الالتفاف لـ setsid |
| `pipe` | 22 | `pipe2` (59) | **مسموح (ALLOW)** | الفلتر لا يحظره |
| `dup2` | 33 | `dup3` (24) | **مسموح (ALLOW)** | الفلتر لا يحظره |
| `poll`, `select` | 7, 23 | `ppoll` (73), `pselect6` (72) | **مسموح (ALLOW)** | الفلتر لا يحظرهما |
| `access` | 21 | `faccessat` (48), `faccessat2` (439) | **مسموح (ALLOW)** | الفلتر لا يحظرهما |
| `readlink`, `unlink`, `mkdir` | 89, 87, 83 | `readlinkat` (78), `unlinkat` (35), `mkdirat` (34) | **مسموح (ALLOW)** | الفلتر لا يحظرها |
| `epoll_wait`, `getdents` | 232, 78 | `epoll_pwait` (22), `getdents64` (61) | **مسموح (ALLOW)** | الفلتر لا يحظرها |

### 2. هل يمكن الالتفاف على حظر `setsid`/`setpgid` عبر استدعاءات حديثة؟
- **`clone` و `clone3`**: لا توجد راية أو حقل في `struct clone_args` يسمح بإنشاء جلسة جديدة (session leader) مكافئة لـ `setsid`. إنشاء عملية جديدة يضعها في نفس الجلسة التابعة للأب.
- **`io_uring`**: واجهة `io_uring` تتيح تنفيذ عمليات الإدخال والإخراج والشبكة والملفات غير المتزامنة؛ لا تملك opcodes في النواة لتغيير الـ session ID أو process group ID.
- **الخلاصة**: حظر `SYS_SETSID` و `SYS_SETPGID` كافٍ لمنع الانفصال، ولا توجد استدعاءات بديلة في AArch64 تؤدي إلى إنشاء جلسة دون المرور بهذين الاستدعائين.

### 3. ما يحميه الـ Sandbox (Bubblewrap) مقابل ما يحميه الفلتر
- **وظيفة الفلتر (`mcp-guard`)**: منع انفصال العمليات فقط (`setsid`/`setpgid`).
- **وظيفة العزل (`bubblewrap`)**: منع الوصول لملفات النظام، ومنع الوصول للشبكة عبر مساحة أسماء شبكية فارغة (`--unshare-net`)، ومنع رؤية العمليات الأخرى (`--unshare-pid`)، وتقييد الذاكرة عبر `nabd-limit` (`RLIMIT_AS`).
- على **Android/Termux**: عزل bubblewrap معطّل صراحة (`!android`) لغياب مساحات الأسماء، بينما يبقى حارس `mcp-guard` متاحاً للبناء ويفشل بشكل مغلق (fail-closed) عند تعذر تثبيت seccomp.
