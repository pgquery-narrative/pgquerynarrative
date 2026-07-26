{{/*
Common labels
*/}}
{{- define "pgquerynarrative.labels" -}}
app.kubernetes.io/name: pgquerynarrative
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Fail install/template when required StrictMode secrets are missing or placeholders.
*/}}
{{- define "pgquerynarrative.validateSecrets" -}}
{{- $pw := .Values.secret.databasePassword | default "" | toString -}}
{{- $ro := .Values.secret.databaseReadonlyPassword | default "" | toString -}}
{{- $hash := .Values.secret.apiKeyHash | default "" | toString -}}
{{- $sess := .Values.secret.sessionSecret | default "" | toString -}}
{{- $enc := .Values.secret.dataEncryptionKey | default "" | toString -}}
{{- if or (eq $pw "") (lt (len $pw) 16) (hasPrefix "changeme" ($pw | lower)) (hasPrefix "change-me" ($pw | lower)) (hasPrefix "replace" ($pw | lower)) }}
{{- fail "secret.databasePassword must be set to a strong non-placeholder value (>=16 chars). See values-production.example.yaml" }}
{{- end }}
{{- if or (eq $ro "") (lt (len $ro) 16) (hasPrefix "changeme" ($ro | lower)) (hasPrefix "change-me" ($ro | lower)) (hasPrefix "replace" ($ro | lower)) }}
{{- fail "secret.databaseReadonlyPassword must be set to a strong non-placeholder value (>=16 chars)." }}
{{- end }}
{{- if or (eq $hash "") (lt (len $hash) 64) (hasPrefix "changeme" ($hash | lower)) (hasPrefix "replace" ($hash | lower)) }}
{{- fail "secret.apiKeyHash must be a SHA-256 hex digest (64 chars). Plaintext SECURITY_API_KEY is rejected in production." }}
{{- end }}
{{- if or (eq $sess "") (lt (len $sess) 32) (hasPrefix "changeme" ($sess | lower)) (hasPrefix "replace" ($sess | lower)) }}
{{- fail "secret.sessionSecret must be set (>=32 chars, non-placeholder)." }}
{{- end }}
{{- if and (eq $enc "") (eq $sess "") }}
{{- fail "secret.dataEncryptionKey or secret.sessionSecret is required for at-rest encryption." }}
{{- end }}
{{- if and (ne $enc "") (or (lt (len $enc) 32) (hasPrefix "changeme" ($enc | lower)) (hasPrefix "replace" ($enc | lower))) }}
{{- fail "secret.dataEncryptionKey must be >=32 chars and non-placeholder when set." }}
{{- end }}
{{- if .Values.security.scheduleRunnerEnabled }}
{{- $hosts := .Values.webhook.allowedHosts | default "" | toString -}}
{{- $wh := .Values.secret.webhookSigningSecret | default "" | toString -}}
{{- if eq $hosts "" }}
{{- fail "webhook.allowedHosts is required when security.scheduleRunnerEnabled=true" }}
{{- end }}
{{- if or (eq $wh "") (lt (len $wh) 16) (hasPrefix "changeme" ($wh | lower)) }}
{{- fail "secret.webhookSigningSecret (>=16 chars) is required when security.scheduleRunnerEnabled=true" }}
{{- end }}
{{- end }}
{{- end }}
