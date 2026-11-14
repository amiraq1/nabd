# نموذج التهديدات — NabdCoder

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
- عزل على مستوى kernel/container؛ لا يوجد sandbox.

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

- **احتواء المسارات والروابط**: `os.Root` على جذر الـworkspace، مع رفض الملفات غير العادية وhard links متعددة عبر descriptor مفتوح؛ فتح FIFO غير حاجب، والبصمات والقراءات المخصصة للتعديل محدودة إلى 2MiB. (اختبارات: `TestJailRejectsEscapes`, `TestJailRejectsSymlinkEscape`, `TestJailRejectsHardLinkToOutsideFile`, `TestFileDigestRejectsFIFOWithoutBlocking`, `TestFileDigestRejectsOversizedFile`.)
- **سياسة المسارات الحساسة**: `.nabd/`, `.git/`, `.env*` (عدا القوالب), `id_*` (عدا `.pub`), وملفات المفاتيح مرفوضة لأدوات الملفات، ومخفية من `list_dir`. (اختبارات: `TestSensitivePathsDenied`, `TestShadowDeniedToFileTools`.)
- **بوابة الموافقة**: preview يعرض diff للمحتوى النهائي بعد `gofmt` مع حفظ CRLF؛ رمز `edit_file` مرتبط ببصمة الأساس المعروضة، وتُفحص عند إصدار الرمز وداخل التنفيذ قبل الكتابة. محارف اتجاه Unicode تُعرض كرموز صريحة. معاينة `mcp_call` لا تعرض قيم السلاسل أو الحقول الحساسة وتمنع الموافقة ما دامت مخفية. (اختبارات: `TestApprovalPreviewShowsActualGoFormattingDiff`, `TestApprovedEditRejectsFileChangedBeforeExecution`, `TestApprovalPreviewEscapesBidiControlsInPathsAndCommands`.)
- **تدقيق إلزامي للكتابة**: `requested`/`approved` fail-closed قبل التنفيذ. حدث `applied` يكتب أولاً في pending journal متزامن ثم إلى السجل؛ `Open` يعيد تشغيله idempotently بعد فشل الكتابة. القراءة تعمل بدون تدقيق. (اختبارات: `TestAppliedEventSurvivesLogFailureAndReplaysOnce`, `TestAppliedAuditFailureQueuesDurableEvent`.)
- **تعديل آمن**: كتابة ذرّية temp+fsync+rename مع حفظ mode، رفض no-op وsymlink والملفات >2MiB، وحفظ CRLF. يحتفظ التعديل بمقبض الملف المفتوح، ويربط رمز الموافقة ببصمة SHA-256 وهوية الملف عند إصدار الرمز؛ ثم يعيد التحقق من الهوية والمحتوى قبل النسخة الاحتياطية وقبل rename. تُسلسل تعديلات وعمليات undo داخل العملية، وتُنشأ نسخة shadow بعد التحقق الأول فقط. (اختبارات: `TestApprovedEditRejectsSameContentFileReplacement`, `TestApprovedEditDetectsReplacementDuringCommit`, `TestApprovedEditDetectsSymlinkSubstitutionDuringCommit`, `TestApprovedEditDetectsHardLinkAddedDuringCommit`, `TestApprovedEditDetectsConcurrentModificationDuringCommit`, `TestApprovedEditWritesExactResultAndUndoRestoresExactOriginal`.)
- **استعادة محكومة**: `undo` يستعيد فقط إذا طابق محتوى الملف `sha256_after`، ويرفض symlink، ويعلّم النسخة `undone`. (اختبارات: `TestUndoRestoresFile`, `TestUndoRefusesAfterDrift`, `TestUndoNoArgRestoresLatest`, `TestUndoIsAudited`.)
- **go_verify محكوم**: بيئة allowlist بلا `PATH` الموروث أو مفاتيح وتهيئات المترجم؛ `GOENV=off`, `CGO_ENABLED=0`, `GOFLAGS=-mod=readonly`, `GOPROXY=off`, `GOTOOLCHAIN=local`. أنماط الحزم workspace-relative. يعمل Linux/Android الأمر داخل مشرف فرعي مستقل (subreaper) لكل أمر؛ وقد يضيف Go نفسه `GOROOT/bin` إلى PATH الخاص بعملية الاختبار. هذا احتواء لإلغاء شجرة العملية وليس sandbox لكود الاختبار. (اختبارات: `TestGoVerifyEnvAllowlist`, `TestBuildVerifyEnvExcludesPathAndBuildOverrides` واختبارات شجرة العمليات أدناه.)
- **UTF-8**: `read_file` يبقي كل rune كاملاً عبر حدود الصفحات، وقد يتجاوز الحد المطلوب حتى 3 بايت. (اختبار: `TestReadFilePagesKeepUTF8RunesIntact`.)
- **احتواء العمليات الخارجية**: على Linux/Android، يُعاد تشغيل البرنامج الموثوق كمشرف فرعي منفصل لكل أمر؛ تُكتشف فقط أحفاد ذلك المشرف، وتُنهى حتى بعد `setsid` أو `setpgid` أو خروج القائد. يستخدم الإنهاء pidfd مع فحص وقت بدء العملية لتفادي قتل PID أُعيد استخدامه. لا توجد لقطة لأبناء Nabd ولا قتل بناءً على PPID الخاص به. يتطلب ذلك دعم kernel لـ`PR_SET_CHILD_SUBREAPER` وpidfd و`/proc`؛ إذا غاب أي منها، يفشل بدء الأمر أو التنظيف مغلقاً ولا يتحول إلى قتل أوسع. (اختبارات: `TestProcessTreeDoesNotKillUnrelatedSibling`, `TestProcessTreeCommandsRunConcurrently`, `TestProcessTreeKillsSessionAndProcessGroupEscapes`, `TestProcessTreeCleansDescendantThatOutlivesLeader`.)
- **LLM مضبوط**: إعداد من البيئة فقط، مهلة كلية، إلغاء عند Ctrl+C، retry محدود على 429/5xx/الشبكة فقط، وميزانية سياق تقديرية مع trim واعٍ للأزواج. (اختبارات: `TestAgentCancelsRequestOnCtrlC`, `TestChatRetriesOn429And5xx`, `TestChatDoesNotRetry400`, `TestTrimHistoryKeepsToolPairs`.)
- **واجهة مقاومة للتزوير**: إزالة ANSI/C0/C1، تهريب الحقول أحادية السطر، ومنع الموافقة على المعاينات المخفية أو المقتطعة. (اختبارات: `TestSanitizeForDisplay`, `TestApprovalPreviewSanitizesANSIText`, `TestApprovalPreviewBlocksSpoofedLineFields`.)
- **mcp-server للقراءة فقط**: لا يعلن أي أداة تتطلب موافقة تفاعلية؛ ينشر `read_file` و`list_dir` فقط. (اختبار: `TestServerIsReadOnlyAndDisablesMCPCall`.)

## 6. ما لا يضمنه النظام

- **لا sandbox**: `go_verify` و`mcp_call` ينفّذان كوداً/عمليات بصلاحيات المستخدم. الموافقة خطوة بشرية، وليست عزلاً.
- **شبكة كود الاختبار**: `GOPROXY=off` يمنع تنزيل وحدات Go فقط؛ كود الاختبار يستطيع فتح sockets مباشرة.
- **`mcp_call`**: الأمر المُدار يحصل على بيئة allowlist صغيرة، لا تتضمن `NABD_API_KEY`. على Linux/Android يشغّله مشرف فرعي مستقل يقتل أحفاده المعتادين عند الإغلاق؛ هذا لا يمنع الأمر من الوصول إلى ملفات المستخدم أو الشبكة، ولا يعزله عن صلاحيات المستخدم. إذا تعذر دعم احتواء الشجرة، يُرفض تشغيله. قيم السلاسل في معاملات MCP مخفية، ولذلك تمنع الموافقة على الاستدعاءات التي تحتوي عليها حتى تتوفر معاينة واعية بمخطط الأداة.
- **تسريب المحتوى إلى LLM**: `read_file` ومخرجات الأدوات تُرسل إلى endpoint الـLLM. إذا كان بعيداً، فمحتوى ملفات المشروع يخرج من الجهاز.
- **سياسة الأسرار بالأسماء فقط**: سر داخل `config.yaml` أو ملف غير مطابق للأنماط يمكن قراءته.
- **سلامة سجل التدقيق**: append-only بالبناء، لكنه ليس tamper-evident؛ مستخدم محلي يستطيع حذف/تفريغ `.nabd/audit.log`. لا hash chain ولا تدوير بعد.
- **TOCTOU داخل الجذر**: يربط رمز الموافقة مسار الملف وبصمته وهوية inode التي فُحصت عند إصدار الرمز. يحتفظ التنفيذ بالـdescriptor، ويقارن `os.SameFile` والبصمة بعد تجهيز الملف المؤقت، قبل إنشاء shadow، ومرة أخرى قبل rename؛ كما تُسلسل التعديلات وundo داخل العملية. مع ذلك لا يوفر rename على الأنظمة المستهدفة مقارنة-واستبدال ذرية مشروطة بـinode أو قفلاً إلزامياً للكتّاب الخارجيين؛ لذلك يبقى احتمال ضيق لتعديل محلي غير متعاون بين آخر تحقق وrename. لا ندّعي إزالة TOCTOU بالكامل.
- **hard links**: أدوات الملفات ترفض قراءة الملفات العادية ذات أكثر من رابط؛ الكتابة الذرّية عبر rename تستبدل الاسم، والأسماء الأخرى المرتبطة تحتفظ بالمحتوى القديم.
- **حدود المنصات**: احتواء العمليات يتطلب Linux/Android مع `/proc` و`PR_SET_CHILD_SUBREAPER` وpidfd؛ عند غياب أي منها لا يبدأ الأمر أو يفشل التنظيف دون الرجوع إلى قتل PPID واسع. Windows والأنظمة غير Linux ترفض `go_verify` و`mcp_call` fail-closed حتى يتوفر احتواء موثوق. بناء Android يستهدف Linux ABI، لكن توافر pidfd يعتمد على kernel الفعلي في الجهاز.
- **Termux DNS/TLS**: غير مؤكدين لثنائي `CGO_ENABLED=0`؛ راجع فحص الجهاز في README.
- **لا حدود معدل/حصص**: لا rate limiting لاستدعاءات الأدوات أو الـLLM.
- **لا LICENSE**: لم يُضف ترخيص قانوني بعد.

## 7. سيناريوهات واختبارات

- **هروب مسار/abs/UNC/NUL** → `TestJailRejectsEscapes`.
- **symlink خارج الجذر** → `TestJailRejectsSymlinkEscape` و`TestEditThroughSymlinkedParentRejected`.
- **hard link إلى ملف خارج المساحة** → `TestJailRejectsHardLinkToOutsideFile`.
- **قراءة أسرار بالاسم** → `TestSensitivePathsDenied`.
- **تجاوز البوابة بلا approver** → `TestRunFailsClosedWithoutApprover`.
- **إعادة استخدام token أو استخدامه لوسائط أخرى** → `TestApprovalTokenRequiredAndSingleUse`.
- **انتهاء token** → `TestApprovalTokenExpires`.
- **تعذّر التدقيق** → `TestAuditLogFailClosedForWrites`.
- **حقن أسطر/إظهار أسرار MCP** → `TestApprovalPreviewBlocksSpoofedLineFields` و`TestApprovalPreviewRedactsMCPSecretsAndMedia`.
- **تزوير ANSI** → `TestSanitizeForDisplay` و`TestApprovalPreviewSanitizesANSIText`.
- **طرفية صغيرة/non-TTY** → `TestTinyWindowDoesNotPanic` و`TestEnsureInteractiveRejectsNonTTY`.
- **تسريب بيئة الاختبار** → `TestGoVerifyEnvAllowlist` و`TestBuildVerifyEnvExcludesPathAndBuildOverrides`.
- **أنماط حزم خارجية** → `TestValidatePkgPattern`.
- **ملكية شجرة العمليات** → `TestProcessTreeKillsNormalChildAndGrandchild`, `TestProcessTreeKillsSessionAndProcessGroupEscapes`, `TestProcessTreeCleansDescendantThatOutlivesLeader`, `TestProcessTreeDoesNotKillUnrelatedSibling`, `TestProcessTreeCommandsRunConcurrently`, `TestProcessTreeCancellationDuringStartup`, `TestProcessTreeCleanupAfterNormalCompletion`, و`TestProcessTreeForcedCancellationCleansAllOwnedDescendants`.
- **تغيير/استبدال الملف بعد الموافقة أو أثناء commit** → `TestApprovedEditRejectsFileChangedBeforeExecution`, `TestApprovedEditRejectsSameContentFileReplacement`, `TestApprovedEditDetectsReplacementDuringCommit`, `TestApprovedEditDetectsSymlinkSubstitutionDuringCommit`, `TestApprovedEditDetectsHardLinkAddedDuringCommit`, و`TestApprovedEditDetectsConcurrentModificationDuringCommit`.
- **UTF-8 عند حد الصفحة** → `TestReadFilePagesKeepUTF8RunesIntact`.
- **فشل audit.log بعد التعديل** → `TestAppliedAuditFailureQueuesDurableEvent` و`TestAppliedEventSurvivesLogFailureAndReplaysOnce`.
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
- **P2**: استبدال مشروط ذرياً بهوية الملف أو قفل نظام ملفات يلتزم به الكتّاب الخارجيون لتضييق نافذة TOCTOU المتبقية.
- **P3**: Job Objects على Windows بدل `taskkill`، وضبط `max_tokens`/`temperature`، وstreaming، وترخيص.

## 9. التحقق على Termux

راجع قسم «التحقق على Termux» في [README](../README.md): كتلة probe واحدة مع المخرجات المتوقعة لـDNS/TLS/HTTPS. النتيجة تُحدد ما إذا كان `CGO_ENABLED=0` يحتاج ضبط `SSL_CERT_FILE` أو مسار DNS محلي.
