{{/*
corndogs.validateStorage aborts the install/upgrade (helm `fail`) when the
file backend is selected together with a multi-replica configuration. The
embedded bbolt data file is owned by a single process and cannot be shared
across pods, so the file backend is single-replica only.
*/}}
{{- define "corndogs.validateStorage" -}}
{{- if eq .Values.storage.backend "file" -}}
  {{- if gt (int .Values.replicaCount) 1 -}}
    {{- fail (printf "\n\ncorndogs: storage.backend=\"file\" cannot run with replicaCount=%d.\nThe embedded bbolt data file is owned by a single process and cannot be\nshared across replicas, so the file backend is single-replica only.\n\nTo fix, pick one:\n  • run a single replica: --set replicaCount=1\n  • use the shared backend: --set storage.backend=postgres\n" (int .Values.replicaCount)) -}}
  {{- end -}}
  {{- if and .Values.autoscaling.enabled (gt (int .Values.autoscaling.maxReplicas) 1) -}}
    {{- fail (printf "\n\ncorndogs: storage.backend=\"file\" cannot run with autoscaling (maxReplicas=%d).\nThe file backend is single-replica only (the bbolt data file is owned by one\nprocess and cannot be shared across pods).\n\nTo fix, pick one:\n  • disable autoscaling: --set autoscaling.enabled=false\n  • use the shared backend: --set storage.backend=postgres\n" (int .Values.autoscaling.maxReplicas)) -}}
  {{- end -}}
{{- else if ne .Values.storage.backend "postgres" -}}
  {{- fail (printf "\n\ncorndogs: storage.backend=%q is not valid. Choose one:\n  • postgres  (default) — shared, horizontally-scalable\n  • file                — embedded, single-replica\n" .Values.storage.backend) -}}
{{- end -}}
{{- end -}}
