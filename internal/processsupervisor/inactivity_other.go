//go:build !linux && !windows && !darwin

package processsupervisor

func sampleProcessActivity(int) processActivitySnapshot {
	return processActivitySnapshot{}
}
