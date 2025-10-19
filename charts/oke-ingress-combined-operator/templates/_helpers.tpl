{{- define "oke.operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.name -}}
{{ .Values.serviceAccount.name }}
{{- else -}}
oke-ingress-combined-operator
{{- end -}}
{{- end -}}
