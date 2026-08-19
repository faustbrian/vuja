package spec

import "sort"

type commandDistance struct {
	name     string
	distance int
}

func NearestExternalCommands(input string, limit int) []string {
	scanExternalCommands()
	names := make([]string, 0, len(pathCmds))
	for name := range pathCmds {
		names = append(names, name)
	}
	return nearestCommandNames(input, names, limit)
}

func nearestCommandNames(input string, candidates []string, limit int) []string {
	if input == "" || limit <= 0 {
		return nil
	}
	distances := make([]commandDistance, 0, len(candidates))
	for _, candidate := range candidates {
		distances = append(distances, commandDistance{
			name:     candidate,
			distance: damerauLevenshtein(input, candidate),
		})
	}
	sort.SliceStable(distances, func(i, j int) bool {
		if distances[i].distance != distances[j].distance {
			return distances[i].distance < distances[j].distance
		}
		return distances[i].name < distances[j].name
	})
	if len(distances) > limit {
		distances = distances[:limit]
	}
	results := make([]string, 0, len(distances))
	for _, distance := range distances {
		results = append(results, distance.name)
	}
	return results
}

func damerauLevenshtein(left, right string) int {
	a, b := []rune(left), []rune(right)
	rows := len(a) + 1
	cols := len(b) + 1
	matrix := make([][]int, rows)
	for i := range matrix {
		matrix[i] = make([]int, cols)
		matrix[i][0] = i
	}
	for j := range cols {
		matrix[0][j] = j
	}
	for i := 1; i < rows; i++ {
		for j := 1; j < cols; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			matrix[i][j] = min(
				matrix[i-1][j]+1,
				matrix[i][j-1]+1,
				matrix[i-1][j-1]+cost,
			)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				matrix[i][j] = min(matrix[i][j], matrix[i-2][j-2]+1)
			}
		}
	}
	return matrix[len(a)][len(b)]
}

func CommandEditDistance(left, right string) int {
	return damerauLevenshtein(left, right)
}
