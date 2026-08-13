package api

import _ "embed"

//go:embed task-plan-openapi.json
var taskPlanOpenAPIDocument []byte

//go:embed task-plan-v1-fixtures.json
var taskPlanV1Fixtures []byte

func TaskPlanDocument() []byte {
	return append([]byte(nil), taskPlanOpenAPIDocument...)
}

func TaskPlanFixtures() []byte {
	return append([]byte(nil), taskPlanV1Fixtures...)
}

func ParseTaskPlanRoutes() ([]Route, error) {
	return parseRoutes(taskPlanOpenAPIDocument)
}
