class DiagnosticCheck {
  final String stage;
  final String status;
  final String address;
  final int elapsedMs;
  final String message;
  final String error;

  const DiagnosticCheck({
    required this.stage,
    required this.status,
    this.address = '',
    this.elapsedMs = 0,
    this.message = '',
    this.error = '',
  });

  factory DiagnosticCheck.fromJson(Map<String, dynamic> json) =>
      DiagnosticCheck(
        stage: json['stage'] as String,
        status: json['status'] as String,
        address: json['address'] as String? ?? '',
        elapsedMs: (json['elapsed_ms'] as num?)?.toInt() ?? 0,
        message: json['message'] as String? ?? '',
        error: json['error'] as String? ?? '',
      );

  String get label => switch (stage) {
    'local_listener' => '本地监听端口',
    'remote_listener' => '远端监听端口',
    'ssh' => 'SSH 连接与认证',
    'target' => '目标 TCP 服务',
    _ => stage,
  };

  String get statusLabel => switch (status) {
    'ok' => '通过',
    'failed' => '未通过',
    'skipped' => '未检查',
    _ => status,
  };
}

class TunnelDiagnostic {
  final String name;
  final String mode;
  final bool ok;
  final int elapsedMs;
  final List<DiagnosticCheck> checks;

  const TunnelDiagnostic({
    required this.name,
    required this.mode,
    required this.ok,
    required this.elapsedMs,
    required this.checks,
  });

  factory TunnelDiagnostic.fromJson(Map<String, dynamic> json) =>
      TunnelDiagnostic(
        name: json['name'] as String,
        mode: json['mode'] as String,
        ok: json['ok'] == true,
        elapsedMs: (json['elapsed_ms'] as num).toInt(),
        checks: List.unmodifiable(
          (json['checks'] as List).map(
            (value) => DiagnosticCheck.fromJson(
              (value as Map).cast<String, dynamic>(),
            ),
          ),
        ),
      );

  String get summary => !ok
      ? '发现连接问题'
      : checks.any((check) => check.status == 'skipped')
      ? '已检查项目通过，另有未检查项'
      : '所有检查通过';

  String toReport() => [
    '隧道诊断：$name（$mode）',
    '$summary · 总耗时 $elapsedMs ms',
    for (final check in checks) ...[
      '',
      '${check.label}：${check.statusLabel}（${check.elapsedMs} ms）',
      if (check.address.isNotEmpty) check.address,
      check.message,
      if (check.error.isNotEmpty) check.error,
    ],
    '',
    '仅检查 TCP 可达性，不验证数据库账号或业务协议。',
  ].join('\n');
}
