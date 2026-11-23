package subtitles

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/transcribe"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

func modelsByID(t *testing.T, s *Service) map[string]ModelView {
	t.Helper()
	out := make(map[string]ModelView)
	for _, m := range s.Models(context.Background()) {
		out[m.ID] = m
	}
	return out
}

func TestModels_RuntimeModes(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)

	t.Run("ai mode GGUF and GGML", func(t *testing.T) {
		t.Setenv("HOLYARRS_OV_SPEECH", "0")
		t.Setenv("HOLYARRS_OV_TRANSLATE", "0")
		t.Setenv("HOLYARRS_OV_PREFER_FP16", "0")
		t.Setenv("HOLYARRS_OV_HYMT_INT4", "0")

		got := modelsByID(t, s)
		cases := []struct {
			id, name, quant, backend, source string
			size                             int64
			need                             uint64
		}{
			{"hy-mt2-1.8b-q8", "Hy-MT2 1.8B", "Q8_0", "GGUF", "tencent/Hy-MT2-1.8B-GGUF", 1908528192, 3 << 30},
			{"gemma-4-e4b-q4", "Gemma 4 E4B", "Q4_0", "GGUF", "ggml-org/gemma-4-E4B-it-GGUF", 4590807392, 7 << 30},
			{"whisper-large-v3-turbo-q8_0", "Whisper large-v3-turbo (Q8_0)", "Q8_0", "GGML", "ggerganov/whisper.cpp", 874188075, 1600 << 20},
			{"whisper-large-v3-turbo-q5_0", "Whisper large-v3-turbo (Q5_0)", "Q5_0", "GGML", "ggerganov/whisper.cpp", 574041195, 1100 << 20},
			{"whisper-large-v3-turbo", "Whisper large-v3-turbo (FP16)", "FP16", "GGML", "ggerganov/whisper.cpp", 1624555275, 2500 << 20},
			{"silero-vad-6.2.0", "Silero voice detection", "FP32", "GGML", "ggml-org/whisper-vad", 885098, 64 << 20},
		}
		for _, tc := range cases {
			mv, ok := got[tc.id]
			if !ok {
				t.Fatalf("missing model %q", tc.id)
			}
			if mv.Name != tc.name || mv.Quantization != tc.quant || mv.Backend != tc.backend || mv.Source != tc.source || mv.Size != tc.size || mv.Need != tc.need {
				t.Fatalf("%s: got %+v, want name=%q quant=%q backend=%q source=%q size=%d need=%d",
					tc.id, mv, tc.name, tc.quant, tc.backend, tc.source, tc.size, tc.need)
			}
		}
		if tr := s.Transcriber(context.Background()); tr.Model != "Whisper large-v3-turbo (Q8_0)" {
			t.Fatalf("Transcriber.Model = %q, want %q", tr.Model, "Whisper large-v3-turbo (Q8_0)")
		}
	})

	t.Run("ai-intel speech OpenVINO and Vulkan translation", func(t *testing.T) {
		t.Setenv("HOLYARRS_OV_SPEECH", "1")
		t.Setenv("HOLYARRS_OV_TRANSLATE", "0")
		t.Setenv("HOLYARRS_OV_PREFER_FP16", "0")
		t.Setenv("HOLYARRS_OV_HYMT_INT4", "0")

		got := modelsByID(t, s)
		if mv := got["hy-mt2-1.8b-q8"]; mv.Backend != "GGUF" || mv.Quantization != "Q8_0" || mv.Source != "tencent/Hy-MT2-1.8B-GGUF" || mv.Size != 1908528192 {
			t.Fatalf("hy-mt2-1.8b-q8 = %+v", mv)
		}
		if mv := got["whisper-large-v3-turbo-q8_0"]; mv.Name != "Whisper large-v3-turbo (INT8)" || mv.Quantization != "INT8" || mv.Backend != "OpenVINO" || mv.Source != "OpenVINO/whisper-large-v3-turbo-int8-ov" || mv.Size != 828096445 {
			t.Fatalf("whisper-large-v3-turbo-q8_0 = %+v", mv)
		}
		if mv := got["whisper-large-v3-turbo-q5_0"]; mv.Name != "Whisper large-v3-turbo (INT4)" || mv.Quantization != "INT4" || mv.Backend != "OpenVINO" || mv.Source != "OpenVINO/whisper-large-v3-turbo-int4-ov" || mv.Size != 477521002 {
			t.Fatalf("whisper-large-v3-turbo-q5_0 = %+v", mv)
		}
		if mv := got["silero-vad-6.2.0"]; mv.Quantization != "ONNX" || mv.Backend != "OpenVINO" || mv.Source != "snakers4/silero-vad" || mv.Size != 2327524 {
			t.Fatalf("silero-vad-6.2.0 = %+v", mv)
		}
		if tr := s.Transcriber(context.Background()); tr.Model != "Whisper large-v3-turbo (INT8)" {
			t.Fatalf("Transcriber.Model = %q, want %q", tr.Model, "Whisper large-v3-turbo (INT8)")
		}
	})

	t.Run("ai-intel prefer FP16", func(t *testing.T) {
		t.Setenv("HOLYARRS_OV_SPEECH", "1")
		t.Setenv("HOLYARRS_OV_TRANSLATE", "0")
		t.Setenv("HOLYARRS_OV_PREFER_FP16", "1")
		t.Setenv("HOLYARRS_OV_HYMT_INT4", "0")

		got := modelsByID(t, s)
		mv := got["whisper-large-v3-turbo-q8_0"]
		if mv.Name != "Whisper large-v3-turbo (FP16)" || mv.Quantization != "FP16" || mv.Backend != "OpenVINO" || mv.Source != "OpenVINO/whisper-large-v3-turbo-fp16-ov" || mv.Size != 1627657898 || mv.Need != 2500<<20 {
			t.Fatalf("whisper-large-v3-turbo-q8_0 with prefer FP16 = %+v", mv)
		}
		if tr := s.Transcriber(context.Background()); tr.Model != "Whisper large-v3-turbo (FP16)" {
			t.Fatalf("Transcriber.Model = %q, want %q", tr.Model, "Whisper large-v3-turbo (FP16)")
		}
	})

	t.Run("ai-intel full OpenVINO", func(t *testing.T) {
		t.Setenv("HOLYARRS_OV_SPEECH", "1")
		t.Setenv("HOLYARRS_OV_TRANSLATE", "1")
		t.Setenv("HOLYARRS_OV_PREFER_FP16", "0")
		t.Setenv("HOLYARRS_OV_HYMT_INT4", "0")

		got := modelsByID(t, s)
		if mv := got["hy-mt2-1.8b-q8"]; mv.Name != "Hy-MT2 1.8B" || mv.Quantization != "INT8" || mv.Backend != "OpenVINO" || mv.Source != "chadouming/Hy-MT2-1.8B-int8-ov" || mv.Size != 1813067334 || mv.Need != 3<<30 {
			t.Fatalf("hy-mt2-1.8b-q8 = %+v", mv)
		}
		if mv := got["gemma-4-e4b-q4"]; mv.Name != "Gemma 4 E4B" || mv.Quantization != "INT4" || mv.Backend != "OpenVINO" || mv.Source != "OpenVINO/gemma-4-E4B-it-int4-ov" || mv.Size != 6537247473 || mv.Need != 8<<30 {
			t.Fatalf("gemma-4-e4b-q4 = %+v", mv)
		}

		mux := http.NewServeMux()
		NewHandlers(f.st, s.Vault, s).Routes(mux)
		code, body := call(mux, "GET", "/api/v1/settings/subtitles/translator/models", "")
		if code != http.StatusOK {
			t.Fatalf("GET models status = %d: %s", code, body)
		}
		var payload struct {
			Models []ModelView `json:"models"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("unmarshal models: %v", err)
		}
		var found bool
		for _, mv := range payload.Models {
			if mv.ID == "hy-mt2-1.8b-q8" {
				found = true
				if mv.Backend != "OpenVINO" || mv.Quantization != "INT8" || mv.Source != "chadouming/Hy-MT2-1.8B-int8-ov" || mv.Size != 1813067334 {
					t.Fatalf("route hy-mt2-1.8b-q8 = %+v", mv)
				}
			}
		}
		if !found {
			t.Fatal("hy-mt2-1.8b-q8 missing in route response")
		}
	})

	t.Run("ai-intel OpenVINO Hy-MT2 INT4", func(t *testing.T) {
		t.Setenv("HOLYARRS_OV_SPEECH", "1")
		t.Setenv("HOLYARRS_OV_TRANSLATE", "1")
		t.Setenv("HOLYARRS_OV_PREFER_FP16", "0")
		t.Setenv("HOLYARRS_OV_HYMT_INT4", "1")

		got := modelsByID(t, s)
		mv := got["hy-mt2-1.8b-q8"]
		if mv.Name != "Hy-MT2 1.8B" || mv.Quantization != "INT4" || mv.Backend != "OpenVINO" || mv.Source != "chadouming/Hy-MT2-1.8B-int4-ov" || mv.Size != 1218806733 || mv.Need != 2<<30 {
			t.Fatalf("hy-mt2-1.8b-q8 INT4 = %+v", mv)
		}

		int4Dir := filepath.Join(s.ModelsDir, "openvino", "ov-hy-mt2-1.8b-int4")
		t.Cleanup(func() { _ = os.RemoveAll(int4Dir) })
		if err := os.MkdirAll(int4Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(int4Dir, "openvino_model.bin"), make([]byte, 4321), 0o644); err != nil {
			t.Fatal(err)
		}
		got = modelsByID(t, s)
		if mv = got["hy-mt2-1.8b-q8"]; mv.Size != 4321 {
			t.Fatalf("hy-mt2-1.8b-q8 INT4 override size = %d, want 4321", mv.Size)
		}
	})
}

func TestModels_OpenVINODirSizeOverride(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)

	t.Setenv("HOLYARRS_OV_SPEECH", "1")
	t.Setenv("HOLYARRS_OV_TRANSLATE", "1")
	t.Setenv("HOLYARRS_OV_PREFER_FP16", "0")
	t.Setenv("HOLYARRS_OV_HYMT_INT4", "0")

	trDir := filepath.Join(s.ModelsDir, "openvino", "ov-hy-mt2-1.8b-int8")
	if err := os.MkdirAll(filepath.Join(trDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trDir, "openvino_model.xml"), make([]byte, 1234), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trDir, "nested", "openvino_model.bin"), make([]byte, 5678), 0o644); err != nil {
		t.Fatal(err)
	}

	got := modelsByID(t, s)
	if mv := got["hy-mt2-1.8b-q8"]; mv.Size != 1234+5678 {
		t.Fatalf("override size = %d, want %d", mv.Size, 1234+5678)
	}
}

func TestModels_CustomGGUFQuantInference(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)
	t.Setenv("HOLYARRS_OV_SPEECH", "0")
	t.Setenv("HOLYARRS_OV_TRANSLATE", "1")
	t.Setenv("HOLYARRS_OV_HYMT_INT4", "0")

	files := map[string]string{
		"custom-IQ4_NL.gguf":   "IQ4_NL",
		"my-model-q4_k_m.gguf": "Q4_K_M",
		"model-bf16.gguf":      "BF16",
		"model-fp16.gguf":      "FP16",
		"model-f16.gguf":       "F16",
		"model-f32.gguf":       "F32",
		"model-mxfp4.gguf":     "MXFP4",
		"plain-model.gguf":     "",
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(s.ModelsDir, name), []byte("gguf"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := modelsByID(t, s)
	for name, wantQuant := range files {
		mv, ok := got[ownPrefix+name]
		if !ok {
			t.Fatalf("missing custom model %q", name)
		}
		if mv.Backend != "GGUF" || mv.Quantization != wantQuant {
			t.Fatalf("%s: backend=%q quant=%q, want backend=GGUF quant=%q", name, mv.Backend, mv.Quantization, wantQuant)
		}
	}
}

func TestUsesOpenVINO_Detection(t *testing.T) {
	t.Run("transcribe sibling and wrapper script", func(t *testing.T) {
		dir := t.TempDir()
		srvPath := filepath.Join(dir, "whisper-server")
		if err := os.WriteFile(srvPath, []byte("#!/bin/sh\necho plain\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(transcribe.EnvServer, srvPath)
		t.Setenv("HOLYARRS_OV_SPEECH", "")

		if transcribe.UsesOpenVINO() {
			t.Fatal("expected false without sibling or script reference")
		}

		// Wrapper script referencing whisper_openvino_server.py in first 512 bytes.
		if err := os.WriteFile(srvPath, []byte("#!/bin/sh\nexec python3 /opt/whisper/whisper_openvino_server.py \"$@\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if !transcribe.UsesOpenVINO() {
			t.Fatal("expected true when script references whisper_openvino_server.py")
		}

		// Env override takes precedence over script inspection.
		t.Setenv("HOLYARRS_OV_SPEECH", "false")
		if transcribe.UsesOpenVINO() {
			t.Fatal("expected false when HOLYARRS_OV_SPEECH=false")
		}
		t.Setenv("HOLYARRS_OV_SPEECH", "")

		// Sibling file detection with plain server binary.
		if err := os.WriteFile(srvPath, []byte("#!/bin/sh\necho plain\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "whisper_openvino_server.py"), []byte("# ov\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !transcribe.UsesOpenVINO() {
			t.Fatal("expected true when sibling whisper_openvino_server.py exists")
		}

		t.Setenv("HOLYARRS_OV_PREFER_FP16", " yes ")
		if !transcribe.OpenVINOPreferFP16() {
			t.Fatal("expected OpenVINOPreferFP16 true")
		}
	})

	t.Run("builtin sibling and wrapper script", func(t *testing.T) {
		dir := t.TempDir()
		runnerPath := filepath.Join(dir, "llama-server")
		if err := os.WriteFile(runnerPath, []byte("#!/bin/sh\necho llama\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(builtin.EnvPath, runnerPath)
		t.Setenv("HOLYARRS_OV_TRANSLATE", "")

		if builtin.UsesOpenVINO() {
			t.Fatal("expected false without sibling or script reference")
		}

		if err := os.WriteFile(runnerPath, []byte("#!/bin/sh\nexec python3 /opt/llama/llm_openvino_server.py \"$@\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if !builtin.UsesOpenVINO() {
			t.Fatal("expected true when script references llm_openvino_server.py")
		}

		t.Setenv("HOLYARRS_OV_TRANSLATE", "no")
		if builtin.UsesOpenVINO() {
			t.Fatal("expected false when HOLYARRS_OV_TRANSLATE=no")
		}
		t.Setenv("HOLYARRS_OV_TRANSLATE", "")

		if err := os.WriteFile(runnerPath, []byte("#!/bin/sh\necho llama\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "llm_openvino_server.py"), []byte("# ov\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !builtin.UsesOpenVINO() {
			t.Fatal("expected true when sibling llm_openvino_server.py exists")
		}

		t.Setenv("HOLYARRS_OV_HYMT_INT4", " YES ")
		if !builtin.OpenVINOHyMTINT4() {
			t.Fatal("expected OpenVINOHyMTINT4 true")
		}
		t.Setenv("HOLYARRS_OV_HYMT_INT4", "off")
		if builtin.OpenVINOHyMTINT4() {
			t.Fatal("expected OpenVINOHyMTINT4 false")
		}
	})
}
