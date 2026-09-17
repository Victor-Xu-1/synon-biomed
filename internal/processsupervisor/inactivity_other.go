//go:build !linux

package processsupervisor

func sampleProcessActivity(int) processActivitySnapshot {
	return processActivitySnapshot{}
}
