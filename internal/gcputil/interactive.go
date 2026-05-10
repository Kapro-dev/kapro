package gcputil

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// SelectProject lists available GCP projects and prompts the user to pick one.
func SelectProject(ctx context.Context) (string, error) {
	fmt.Fprintln(os.Stderr, "Scanning GCP projects...")
	projects, err := ListProjects(ctx)
	if err != nil {
		return "", err
	}
	if len(projects) == 0 {
		return "", fmt.Errorf("no GCP projects found (check ADC/WI credentials)")
	}

	fmt.Fprintf(os.Stderr, "\nFound %d projects:\n\n", len(projects))
	for i, p := range projects {
		fmt.Fprintf(os.Stderr, "  %3d) %-40s  %s\n", i+1, p.ID, p.Name)
	}
	fmt.Fprintln(os.Stderr)

	idx, err := promptNumber("Select project", len(projects))
	if err != nil {
		return "", err
	}
	return projects[idx].ID, nil
}

// SelectCluster lists GKE clusters in a project and prompts the user to pick one.
// Returns (clusterName, location).
func SelectCluster(ctx context.Context, project string) (string, string, error) {
	fmt.Fprintf(os.Stderr, "Scanning GKE clusters in %s...\n", project)
	clusters, err := ListClusters(ctx, project)
	if err != nil {
		return "", "", err
	}
	if len(clusters) == 0 {
		return "", "", fmt.Errorf("no GKE clusters found in project %s", project)
	}

	fmt.Fprintf(os.Stderr, "\nFound %d clusters:\n\n", len(clusters))
	fmt.Fprintf(os.Stderr, "  %3s  %-25s %-20s %-10s %-15s %s\n", "#", "NAME", "LOCATION", "STATUS", "VERSION", "NODES")
	for i, c := range clusters {
		mode := ""
		if c.Autopilot {
			mode = " (autopilot)"
		}
		fmt.Fprintf(os.Stderr, "  %3d) %-25s %-20s %-10s %-15s %d%s\n",
			i+1, c.Name, c.Location, c.Status, c.Version, c.NodeCount, mode)
	}
	fmt.Fprintln(os.Stderr)

	idx, err := promptNumber("Select cluster", len(clusters))
	if err != nil {
		return "", "", err
	}
	return clusters[idx].Name, clusters[idx].Location, nil
}

func promptNumber(label string, max int) (int, error) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Fprintf(os.Stderr, "%s [1-%d]: ", label, max)
		input, err := reader.ReadString('\n')
		if err != nil {
			return 0, fmt.Errorf("read input: %w", err)
		}
		input = strings.TrimSpace(input)
		n, err := strconv.Atoi(input)
		if err != nil || n < 1 || n > max {
			fmt.Fprintf(os.Stderr, "  Invalid choice. Enter a number between 1 and %d.\n", max)
			continue
		}
		return n - 1, nil
	}
}
