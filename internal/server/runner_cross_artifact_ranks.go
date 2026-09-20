package server

type runnerCrossArtifactRankPair struct {
	score string
	rank  string
}

var runnerCrossArtifactScenarioRankPairs = []runnerCrossArtifactRankPair{
	{score: "score_pessimistic", rank: "rank_pessimistic"},
	{score: "score_neutral", rank: "rank_neutral"},
	{score: "score_optimistic", rank: "rank_optimistic"},
}
