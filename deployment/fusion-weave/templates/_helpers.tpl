{{/*
SPDX-License-Identifier: GPL-3.0-or-later
Common labels applied to every resource.
*/}}
{{- define "fusion-weave.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels used by the Deployment and Service.
*/}}
{{- define "fusion-weave.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Full image reference for the operator.
*/}}
{{- define "fusion-weave.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag }}
{{- end }}

{{/*
Full image reference for the API server.
Falls back to the operator image when api.image.repository is not set.
*/}}
{{- define "fusion-weave.api.image" -}}
{{- $repo := default .Values.image.repository .Values.api.image.repository -}}
{{- $tag  := default .Values.image.tag .Values.api.image.tag -}}
{{ $repo }}:{{ $tag }}
{{- end }}

{{/*
Selector labels for the API server Deployment / Service.
*/}}
{{- define "fusion-weave.api.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}-api
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Full image reference for the backup CronJob.
Falls back to the operator image when backup.image.repository is not set.
*/}}
{{- define "fusion-weave.backup.image" -}}
{{- $backup := .Values.backup | default dict -}}
{{- $backupImage := $backup.image | default dict -}}
{{- $repo := default .Values.image.repository $backupImage.repository -}}
{{- $tag  := default .Values.image.tag $backupImage.tag -}}
{{ $repo }}:{{ $tag }}
{{- end }}

{{/*
Default S3 key prefix for CRD backups: "<namespace>/backups" — independent of any
other S3 prefix convention, computed only when backup.s3.backupPrefix is left empty.
*/}}
{{- define "fusion-weave.backup.s3BackupPrefix" -}}
{{- $backup := .Values.backup | default dict -}}
{{- $s3 := $backup.s3 | default dict -}}
{{- if $s3.backupPrefix -}}
{{ $s3.backupPrefix }}
{{- else -}}
{{ printf "%s/backups" .Release.Namespace }}
{{- end -}}
{{- end }}

{{/*
Name of the Secret carrying static AWS credentials for the backup CronJob —
either the chart-created Secret or an existingSecret override.
*/}}
{{- define "fusion-weave.backup.s3SecretName" -}}
{{- $backup := .Values.backup | default dict -}}
{{- $s3 := $backup.s3 | default dict -}}
{{- if $s3.existingSecret -}}
{{- $s3.existingSecret -}}
{{- else -}}
{{- printf "%s-backup-s3-secret" .Release.Name -}}
{{- end -}}
{{- end }}
