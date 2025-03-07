package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/google/pprof/profile"
	"github.com/rivo/tview"
)

// Node represents a node in the flamegraph
type FlameNode struct {
	Name      string
	Value     int64
	Children  map[string]*FlameNode
	Parent    *FlameNode
	Collapsed bool
}

func showTerminalFlamegraph(ctx context.Context, params *queryProfileParams) error {
	// Parse the profile
	f, err := os.Open("/Users/christian/Downloads/profile001.pb.gz")
	if err != nil {
		return fmt.Errorf("failed to open temporary file: %w", err)
	}
	defer f.Close()

	prof, err := profile.Parse(f)
	if err != nil {
		return fmt.Errorf("failed to parse profile: %w", err)
	}

	// Build the flamegraph tree
	root := buildFlameGraph(prof)

	// Create the terminal UI
	return showFlameGraphUI(root)
}

// fetchProfile retrieves the profile data using the existing query functionality
func fetchProfile(ctx context.Context, params *queryProfileParams) ([]byte, error) {
	// Create a pipe to capture the output
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create pipe: %w", err)
	}

	// Save the original output and restore it later
	originalOutput := output(ctx)
	defer func() {
		ctx = withOutput(ctx, originalOutput)
	}()

	// Redirect output to our pipe
	ctx = withOutput(ctx, w)

	// Call the existing queryProfile function with "raw" output
	err = queryProfile(ctx, params, "raw")
	w.Close()
	if err != nil {
		r.Close()
		return nil, err
	}

	// Read the profile data
	profileData, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read profile data: %w", err)
	}

	return profileData, nil
}

// buildFlameGraph builds a flamegraph tree from a profile
func buildFlameGraph(prof *profile.Profile) *FlameNode {
	root := &FlameNode{
		Name:     "root",
		Value:    0,
		Children: make(map[string]*FlameNode),
	}

	// Process each sample in the profile
	for _, sample := range prof.Sample {
		value := int64(sample.Value[0]) // Use the first value (usually CPU time)
		root.Value += value

		// Start from the root for each stack trace
		current := root

		// Process the stack trace from bottom to top (leaf to root)
		// This is the typical way to build a flamegraph
		for i := len(sample.Location) - 1; i >= 0; i-- {
			loc := sample.Location[i]

			// Get function name from the location
			var funcName string
			if len(loc.Line) > 0 && loc.Line[0].Function != nil {
				funcName = loc.Line[0].Function.Name
			} else {
				funcName = fmt.Sprintf("0x%x", loc.Address)
			}

			// Create or update the child node
			child, exists := current.Children[funcName]
			if !exists {
				child = &FlameNode{
					Name:     funcName,
					Value:    0,
					Children: make(map[string]*FlameNode),
					Parent:   current,
				}
				current.Children[funcName] = child
			}

			child.Value += value
			current = child
		}
	}

	return root
}

// showFlameGraphUI displays the flamegraph in a terminal UI
func showFlameGraphUI(root *FlameNode) error {
	app := tview.NewApplication()

	// Create a tree view for the flamegraph
	tree := tview.NewTreeView()
	rootNode := tview.NewTreeNode(fmt.Sprintf("%s (%d)", root.Name, root.Value)).
		SetSelectable(true).
		SetExpanded(true).
		SetReference(root)

	tree.SetRoot(rootNode)
	tree.SetCurrentNode(rootNode)
	tree.SetTitle("Flamegraph").SetBorder(true)

	// Populate the tree with the flamegraph data
	populateTree(rootNode, root)

	// Create a text view for details
	details := tview.NewTextView()
	details.SetDynamicColors(true)
	details.SetRegions(true)
	details.SetWordWrap(true)
	details.SetTitle("Details")
	details.SetBorder(true)

	// Create a help text view
	help := tview.NewTextView().
		SetDynamicColors(true).
		SetText("↑/↓: Navigate  Enter: Expand/Collapse  q: Quit  h: Toggle Help").
		SetTextAlign(tview.AlignCenter).
		SetBorder(true)

	// Create a flex layout
	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(tree, 0, 1, true).
		AddItem(details, 10, 1, false).
		AddItem(help, 1, 1, false)

	// Handle tree selection changes
	tree.SetChangedFunc(func(node *tview.TreeNode) {
		if node == nil {
			return
		}

		flameNode, ok := node.GetReference().(*FlameNode)
		if !ok {
			return
		}

		// Update details view
		details.SetText("") // Clear the text view

		// Build the details text
		detailsText := fmt.Sprintf("Function: %s\n", flameNode.Name)
		detailsText += fmt.Sprintf("Value: %d\n", flameNode.Value)

		if flameNode.Parent != nil {
			percentage := float64(flameNode.Value) / float64(flameNode.Parent.Value) * 100
			detailsText += fmt.Sprintf("Percentage of parent: %.2f%%\n", percentage)
		}

		if root.Value > 0 {
			percentage := float64(flameNode.Value) / float64(root.Value) * 100
			detailsText += fmt.Sprintf("Percentage of total: %.2f%%\n", percentage)
		}

		childCount := len(flameNode.Children)
		detailsText += fmt.Sprintf("Children: %d\n", childCount)

		// Set the text to the details view
		details.SetText(detailsText)
	})

	// Handle key events
	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyRune:
			switch event.Rune() {
			case 'q':
				app.Stop()
				return nil
			case 'h':
				// Toggle help visibility
				if flex.GetItemCount() == 3 {
					flex.RemoveItem(help)
				} else {
					flex.AddItem(help, 1, 1, false)
				}
				return nil
			}
		case tcell.KeyEnter:
			// Expand or collapse the selected node
			node := tree.GetCurrentNode()
			if node != nil {
				node.SetExpanded(!node.IsExpanded())
				return nil
			}
		}
		return event
	})

	// Run the application
	if err := app.SetRoot(flex, true).EnableMouse(true).Run(); err != nil {
		return err
	}

	return nil
}

// populateTree recursively populates the tree view with flamegraph nodes
func populateTree(treeNode *tview.TreeNode, flameNode *FlameNode) {
	// Sort children by value (descending)
	type childPair struct {
		name string
		node *FlameNode
	}

	children := make([]childPair, 0, len(flameNode.Children))
	for name, child := range flameNode.Children {
		children = append(children, childPair{name, child})
	}

	sort.Slice(children, func(i, j int) bool {
		return children[i].node.Value > children[j].node.Value
	})

	// Add children to the tree
	for _, child := range children {
		// Create a visual bar representing the value proportion
		percentage := float64(child.node.Value) / float64(flameNode.Value)
		barWidth := int(percentage * 40) // Use 40 characters as max width
		bar := strings.Repeat("█", barWidth)

		// Format the node text with the value and visual bar
		nodeText := fmt.Sprintf("%s (%d) %s", child.name, child.node.Value, bar)

		childTreeNode := tview.NewTreeNode(nodeText).
			SetSelectable(true).
			SetExpanded(false).
			SetReference(child.node)

		treeNode.AddChild(childTreeNode)

		// Recursively add grandchildren
		populateTree(childTreeNode, child.node)
	}
}
