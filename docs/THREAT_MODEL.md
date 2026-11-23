# نموذج التهديدات — NabdCoder

آخر تحديث لهذا الملف في القسم الأخير يصحّح الأقسام الأقدم: التنفيذ الخارجي معطّل افتراضياً، ويوجد عزل اختياري مثبت على Linux فقط.

هذا المستند يحدد ما يحاول NabdCoder حمايته، ومن هم الخصوم، وما الذي يضمنه النظام فعلياً، وما لا يضمنه. يعتمد على الكود والاختبارات في هذا المستودع، وليس على نوايا تصميمية غير منفذة.

## 1. النطاق

يشمل:

- وكيل `cmd/agent` وأدواته في `internal/tools`.
- خادم `cmd/mcp-server` (للقراءة فقط).
- التخزين المحلي: `.nabd/shadow` و`.nabd/audit.log`.
- عميل LLM في `internal/llm` وبيئة التشغيل.

لا يشمل:

- أمان نظام Termux/Android نفسه، أو صلاحيات التطبيق.
- أمان خادم LLM البعيد أو مزوّد الشبكة.
- أمان مستودع Git البعيد أو حسابات المطوّرين.
- عزل kernel/container خارج نطاق الواجهة؛ العزل الاختياري الموصوف أدناه يعتمد على bubblewrap المثبت محلياً ولا يُعتبر ضماناً على Termux أو Windows.

## 2. الأصول

- **ملفات المشروع**: سرية وسلامة (كود، إعدادات، بيانات).
- **الأسرار**: `NABD_API_KEY`, متغيرات البيئة، ملفات المفاتيح، `.env`, `.git/config`.
- **سجل التدقيق ونسخ shadow**: سلامة السجل وقابلية الاستعادة.
- **الطرفية**: منع تزوير الواجهة أو مطالبات الموافقة.
- **الجهاز**: الذاكرة والقرص (Termux محدود الموارد).

## 3. حدود الثقة

1. **المستخدم ↔ TUI**: المستخدم موثوق؛ الطرفية قد تكون غير موثوقة إذا كانت مشتركة.
2. **TUI ↔ endpoint LLM**: الـendpoint غير موثوق؛ كل رسالة تُرسل قد تحمل محتوى workspace.
3. **النموذج ↔ الأدوات**: النموذج غير موثوق؛ قد يكون مخترقاً أو متأثراً بـprompt injection.
4. **الأدوات ↔ workspace**: المسارات والملفات غير موثوقة؛ قد تحتوي symlinks أو محتوى مضللاً.
5. **go_verify ↔ كود المشروع**: كود الاختبار غير موثوق؛ ينفّذ بصلاحيات المستخدم.
6. **mcp_call ↔ عمليات خارجية**: binary خارجي غير موثوق؛ يعمل بصلاحيات المستخدم.
7. **mcp-server ↔ MCP client**: العميل غير موثوق؛ الخادم يقيّد نفسه للقراءة فقط.

## 4. الخصوم والقدرات

- **نموذج مخترق أو prompt injection**: يستطيع طلب أي أداة، وصياغة وسائط، وإخفاء تعليمات داخل الملفات أو مخرجات الأدوات.
- **ملفات مشروع خبيثة**: ANSI/control sequences، مسارات ملتوية، symlinks، أسماء أسرار، مخرجات أدوات مضللة.
- **مستخدم محلي آخر**: يقرأ/يحذف `.nabd`، يعرف مسارات الملفات، ولا يملك elevated privileges.
- **endpoint بعيد**: يرى الرسائل كاملة، وقد يحتفظ بها.
- **عملية MCP خارجية**: قد تنفّذ أي شيء عند تشغيلها، بما في ذلك الوصول للشبكة.
- **كود اختبار خبيث**: يُنفَّذ عبر `go_verify` ويمكنه قراءة الملفات وفتح sockets.

## 5. ما يضمنه النظام

- **احتواء المسارات**: `os.Root` على جذر الـworkspace لكل عمليات الملفات، مع تحقق إضافي من `..` والمسارات المطلقة وvolume-relative وNUL. `edit_file` و`undo` يرفضان symlink. (اختبارات: `TestJailRejectsEscapes`, `TestJailRejectsSymlinkEscape`, `TestEditThroughSymlinkedParentRejected`.)
- **فتح القراءة على Linux/Android**: فتح نسبي إلى واصف الجذر، مع `O_NOFOLLOW` لكل مكوّن وفتح غير حاجب. هذا لا يجعل جميع عمليات الكتابة مقاومة لسباقات الروابط أو إعادة تسمية المجلدات؛ لا يُدّعى الضمان نفسه للمنصات الأخرى.
- **سياسة المسارات الحساسة**: `.nabd/`, `.git/`, `.env*` في كل مكوّن (عدا ملفات `.env.example` و`.env.sample` و`.env.template` و`.env.dist` النهائية؛ لا يُفحص محتواها), `id_*` (عدا `.pub`), وملفات المفاتيح مرفوضة لأدوات الملفات، ومخفية من `list_dir`. (اختبارات: `TestSensitivePathsDenied`, `TestShadowDeniedToFileTools`.)
- **بوابة الموافقة**: preview يعرض المسار وdiff وSHA-256، وإعادة فحص hash الأساس قبل التنفيذ. `RunApproved` يتطلب token أحادي الاستخدام مربوطاً بالأداة و`args_sha256`، صالح 5 دقائق. `Run` بدون approver يفشل مغلقاً. (اختبارات: `TestApprovalPreviewEdit`, `TestApprovalGuardDetectsFileChange`, `TestRunFailsClosedWithoutApprover`, `TestApprovalTokenRequiredAndSingleUse`, `TestApprovalTokenExpires`.)
- **تدقيق إلزامي للكتابة**: `requested`/`approved` fail-closed قبل التنفيذ؛ تعذّر الكتابة يمنع التعديل/التنفيذ. القراءة تعمل بدون تدقيق. (اختبارات: `TestAuditLogFailClosedForWrites`, `TestReadOnlyToolsSurviveAuditFailure`, `TestAuditLogApprovalFlow`.)
- **تعديل آمن**: كتابة ذرّية temp+fsync+rename مع حفظ mode، رفض no-op وsymlink والملفات >2MiB، CRLF محفوظ، ونسخة shadow قبل التعديل. (اختبارات: `TestEditAtomicPreservesModeAndLeavesNoTemp`, `TestEditPreservesCRLFInGo`, `TestEditRejectsSymlink`, `TestEditRejectsLargeFile`.)
- **استعادة محكومة**: `undo` يستعيد فقط إذا طابق محتوى الملف `sha256_after`، ويرفض symlink، ويعلّم النسخة `undone`. (اختبارات: `TestUndoRestoresFile`, `TestUndoRefusesAfterDrift`, `TestUndoNoArgRestoresLatest`, `TestUndoIsAudited`.)
- **go_verify محكوم**: بيئة allowlist بلا مفاتيح أو proxy، `GOFLAGS=-mod=readonly`, `GOPROXY=off`, `GOTOOLCHAIN=local`، أنماط حزم workspace-relative، مهلة 90s توقف مجموعة العمليات على Unix؛ قد تفلت العمليات المنفصلة ولا يضمن ذلك عزل الكود، ونتيجة منظمة. (اختبارات: `TestGoVerifyEnvAllowlist`, `TestValidatePkgPattern`, `TestGoVerifyTimeout`, `TestProcessGroupKill` على Unix.)
- **LLM مضبوط**: إعداد من البيئة فقط، مهلة كلية، إلغاء عند Ctrl+C، retry محدود على 429/5xx/الشبكة فقط، وميزانية سياق تقديرية مع trim واعٍ للأزواج. (اختبارات: `TestAgentCancelsRequestOnCtrlC`, `TestChatRetriesOn429And5xx`, `TestChatDoesNotRetry400`, `TestTrimHistoryKeepsToolPairs`.)
- **واجهة مقاومة للتزوير**: إزالة ANSI/C0/C1 قبل العرض، clamp للطرفية، وخروج عند non-TTY. (اختبارات: `TestSanitizeForDisplay`, `TestApprovalPreviewSanitizesANSIText`, `TestTinyWindowDoesNotPanic`, `TestEnsureInteractiveRejectsNonTTY`.)
- **حماية MCP الخارجي**: الحارس المطلوب بجانب الوكيل يحجب `setsid` و`setpgid` على Linux/Android المدعومين، وتُحجب البيئة إلا قائمة محددة ويُحد النقل الوارد بـ1MiB. هذه ليست عزلة ملفات أو شبكة. (اختبارات: `TestF24RealMCPCallAndEnvironment`, `TestF24SeccompBlocksSetsid`, `TestF24SeccompBlocksSetpgid`, `TestF24WireLimit`.)
- **mcp-server للقراءة فقط**: يرفض الأدوات غير read-only لغياب approver، و`mcp_call` معطّل. (اختبار: `TestServerIsReadOnlyAndDisablesMCPCall`.)

## 6. ما لا يضمنه النظام

- **لا sandbox**: `go_verify` و`mcp_call` ينفّذان كوداً/عمليات بصلاحيات المستخدم. الموافقة خطوة بشرية، وليست عزلاً.
- **شبكة كود الاختبار**: `GOPROXY=off` يمنع تنزيل وحدات Go فقط؛ كود الاختبار يستطيع فتح sockets مباشرة.
- **`mcp_call`**: يشغّل binary بصلاحيات المستخدم، بلا sandbox للملفات أو الشبكة؛ `command` لا يدعم وسائط. seccomp يمنع syscallين فقط، ولا يضمن منع كل الانفصالات أو الهجمات عبر namespaces أو خدمات خارجية. لا يعتمد على `NABD_MCP_GUARD` لاستبدال الحارس. هوية الملف التنفيذي والحارس غير مرتبطة بالموافقة؛ يمكن تبديلهما بعد عرض الأمر وقبل التنفيذ.
- **تسريب المحتوى إلى LLM**: `read_file` يتطلب موافقة قبل إرسال الناتج؛ قد تكشف أدوات أخرى والرسائل المكتوبة بيانات للـendpoint. إذا كان بعيداً، تخرج البيانات المسموح بها من الجهاز.
- **سياسة الأسرار بالأسماء فقط**: سر داخل `config.yaml` أو ملف غير مطابق للأنماط يمكن قراءته.
- **سلامة سجل التدقيق**: append-only بالبناء، لكنه ليس tamper-evident؛ مستخدم محلي يستطيع حذف/تفريغ `.nabd/audit.log`. لا hash chain ولا تدوير بعد.
- **TOCTOU داخل الجذر**: بين preview والتنفيذ قد يتغيّر محتوى الملف داخل الـworkspace. hash guard + التطابق الفريد يخففان، لكن لا يوجد قفل ملف.
- **hard links**: الكتابة الذرّية عبر rename تستبدل الاسم؛ الأسماء الأخرى المرتبطة تحتفظ بالمحتوى القديم.
- **Windows**: `taskkill /T` أفضل جهد، والصلاحيات approximated.
- **Termux DNS/TLS**: غير مؤكدين لثنائي `CGO_ENABLED=0`؛ راجع فحص الجهاز في README.
- **لا حدود معدل/حصص**: لا rate limiting لاستدعاءات الأدوات أو الـLLM.
- **لا LICENSE**: لم يُضف ترخيص قانوني بعد.

## 7. سيناريوهات واختبارات

- **هروب مسار/abs/UNC/NUL** → `TestJailRejectsEscapes`.
- **symlink خارج الجذر** → `TestJailRejectsSymlinkEscape` و`TestEditThroughSymlinkedParentRejected`.
- **قراءة أسرار بالاسم** → `TestSensitivePathsDenied`.
- **تجاوز البوابة بلا approver** → `TestRunFailsClosedWithoutApprover`.
- **إعادة استخدام token أو استخدامه لوسائط أخرى** → `TestApprovalTokenRequiredAndSingleUse`.
- **انتهاء token** → `TestApprovalTokenExpires`.
- **تعذّر التدقيق** → `TestAuditLogFailClosedForWrites`.
- **تزوير ANSI** → `TestSanitizeForDisplay` و`TestApprovalPreviewSanitizesANSIText`.
- **طرفية صغيرة/non-TTY** → `TestTinyWindowDoesNotPanic` و`TestEnsureInteractiveRejectsNonTTY`.
- **تسريب بيئة الاختبار** → `TestGoVerifyEnvAllowlist`.
- **أنماط حزم خارجية** → `TestValidatePkgPattern`.
- **تعليق شجرة عمليات** → `TestProcessGroupKill` (Unix) و`TestGoVerifyTimeout` (الكل).
- **حلقة أدوات لا نهائية/متكررة** → `TestMaxIterationsSynthesizesToolErrors` و`TestRepeatedCallsBlocked`.
- **إلغاء طلب LLM** → `TestAgentCancelsRequestOnCtrlC`.
- **retry غير صحيح** → `TestChatRetriesOn429And5xx` و`TestChatDoesNotRetry400`.
- **كسر أزواج tool calls** → `TestTrimHistoryKeepsToolPairs`.
- **تعديل غير ذرّي/فقدان mode** → `TestEditAtomicPreservesModeAndLeavesNoTemp`.
- **undo بعد تغيير الملف** → `TestUndoRefusesAfterDrift`.
- **خادم MCP يعدّل ملفاً** → `TestServerIsReadOnlyAndDisablesMCPCall`.

## 8. المخاطر المتبقية وأولوياتها

- **P1**: عزل/allowlist لـ`mcp_call`، وsandbox أو تقييد شبكي/ملفي لـ`go_verify` إذا كان النموذج غير موثوق.
- **P2**: سلسلة hash (`prev_hash`/`entry_hash`) وتدوير سجل التدقيق.
- **P2**: كشف/تنقيح الأسرار بالمحتوى لا بالاسم.
- **P2**: `os.Root` + قفل/معاملة تغلق TOCTOU بين preview والتنفيذ.
- **P3**: Job Objects على Windows بدل `taskkill`، وضبط `max_tokens`/`temperature`، وstreaming، وترخيص.

## 9. التحقق على Termux

راجع قسم «التحقق على Termux» في [README](../README.md): كتلة probe واحدة مع المخرجات المتوقعة لـDNS/TLS/HTTPS. النتيجة تُحدد ما إذا كان `CGO_ENABLED=0` يحتاج ضبط `SSL_CERT_FILE` أو مسار DNS محلي.


## تحديث أمني: فرع العزل والإفشاء (يُلغي ما سبق عند التعارض)

### التنفيذ الخارجي

- `go_verify` و`mcp_call` معطّلتان افتراضياً. لا تُعرضان إلا إذا وُجدت قائمة خوادم MCP مملوكة للمستخدم (`NABD_MCP_CONFIG`)، وتم تفعيل `NABD_EXEC_SANDBOX=1`، ونجح `sandbox.Prove` في نفس التشغيل. أي فشل أو غياب أداة يبقي الأداتين غير موجودتين في سجل الأدوات.
- العزل المُثبت على Linux يستخدم bubblewrap مع user/pid/net/ipc/uts namespace، وجذر شبه فارغ، وربط workspace للقراءة فقط لـMCP وللكتابة لـ`go_verify`، و`/proc` وtmpfs `/tmp`، و`--die-with-parent`. يضبط `cmd/nabd-limit` حدود `RLIMIT_AS` و`RLIMIT_NPROC` قبل exec. يُثبت `cmd/nabd-sandbox-probe` من داخل العزل أنه لا يرى `/etc/passwd`، ولا يستطيع الكتابة في منطقة للقراءة فقط، ولا يفتح socket، ويرى الحدود المطلوبة. (اختبارات: `TestLinuxSandboxProof`، `TestLinuxSandboxOptInPath`، `TestLinuxWrapConstructsConfinedCommand`.)
- `mcp_call` لا يقبل مسار executable من النموذج: يختار اسم خادم من القائمة فقط. تُربط الموافقة ببصمة الخادم والحارس وأي حارس مستخدم والبيئة، وتُعاد البصمات قبل التنفيذ. لا تُمرَّر متغيرات البيئة إلا بقائمة صريحة. (اختبارات: `TestMCPAllowlist*`, `TestMCPApprovalIsBoundToConfiguredServerIdentity`.)
- **ما لا يثبته هذا العزل**: لا يوجد `fexecve` في Go هنا، لذا تبقى نافذة قصيرة بين إعادة التحقق والتنفيذ يمكن لمستخدم محلي فيها تبديل ملف. لا يُقدَّم العزل على Windows أو Termux أو الأنظمة التي لا يتوفر فيها bubblewrap؛ قتل المجموعة لا يضمن عملية انفصلت إلى جلسة أخرى؛ `os.Root` والموافقة وseccomp ليست بديلاً عن العزل.

### الإفشاء إلى النموذج

- كل ناتج أداة يُحتجز محلياً ويُعرض مع الوجهة والوسائط والمخرجات بصيغة `QuoteToASCII`، مع علامة `[END]` وإلزام بالتمرير إلى نهاية العرض قبل الموافقة. تغيير حجم الطرفية يُعيد المراجعة إلى البداية. تُرفض المخرجات التي تحتوي تحكمات أو أحرف اتجاه غير قابلة للعرض بأمان. (اختبارات: `TestAllToolOutputsRequireDisclosure`, `TestFileDisclosureResizeRequiresRereview`, `TestDisclosureFailsClosedOnDeceptiveCharacters`.)
- رفض HTTP البعيد وإعادة التوجيه يبقى كما هو. كشف الأسرار بالمحتوى إرشادي وغير شامل وموثّق كذلك؛ لا يُقدَّم كضمان. رسائل المستخدم وأي قناة جديدة يجب أن تُقيَّم على حدة.

### الكتابة والاستعادة

- قفل `.nabd/edit.lock` عبر `flock`/`LockFileEx` يمنع تحرير أو استعادة متزامنين من عمليتي Nabd؛ القفل داخل العملية لا يزال `writeMu`. (اختبار: `TestCrossProcessLockRefusesConcurrentEdit`.)
- البايتات المعاينة والمكتوبة تمر عبر تحويل واحد؛ بصمتا الإدخال والإخراج يُعاد فحصهما قبل الاستبدال. فشل `fsync` للمجلد بعد rename يُبلَّغ كتحذير وتبقى النسخة الاحتياطية قابلة للاستعادة؛ فشل rename يترك الملف الأصلي بلا ملفات مؤقتة. (اختبارات: `TestDirectorySyncFailureKeepsRestoreAvailable`, `TestReplacementFailureLeavesOriginalAndNoTemp`.)
- **حدود ذرّية**: لا يوجد compare-and-swap مقابل كاتب خارجي بين الفحص النهائي وrename، ولا مزامنة دليل على غير Unix. لا يمكن ضمان الذرّية أمام كاتب خارجي أو انقطاع الطاقة على تلك المنصات.

### التدقيق والاحتفاظ

- السجل مربوط بسلسلة `prev_hash`/`hash` مع حدث `executing` قبل كل عملية كتابة؛ فشل تسجيل النتيجة بعد التنفيذ لا يمحو سجل البدء. الوسائط النصية تُطهَّر من التحكمات وأحرف الاتجاه. (اختبارات: `TestHashChainDetectsTampering`, `TestExecutingEventSurvivesResultLoggingGap`.)
- **`مرساة موثوقة`**: السلسلة تكشف العبث فقط إذا حُفظ `NABD_AUDIT_ANCHOR` خارج متناول من يُراقَب. مستخدم محلي يملك السجل والمرساة يستطيع إعادة حسابها بالكامل؛ لا توقيع خارجي ولا تدوير.
- Shadow يُنظَّف دورياً أثناء تشغيل العملية (افتراضياً كل 24 ساعة) بقاعدة 7 أيام/20 نسخة، مع حذف البيانات الوصفية أولاً حتى لا تظهر نسخة بلا نسخة احتياطية. لا يوجد موعد حذف مضمون بعد إيقاف العملية، ولا تنسيق مع عملية Nabd أخرى. (اختبارات: `TestScheduledCleanupRemovesExpiredWithoutBreakingRecentRestore`, `TestRetentionLoopStopsAndIsCancellable`.)
