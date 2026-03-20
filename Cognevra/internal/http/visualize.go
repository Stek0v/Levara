package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/cognevra/pkg/graphdb"
)

// GraphVisualizationConfig holds Neo4j connection for graph visualization.
type GraphVisualizationConfig struct {
	Neo4jURL      string
	Neo4jUser     string
	Neo4jPassword string
	Neo4jDatabase string
}

// GraphNodeDTO matches Cognee's frontend expected format.
type GraphNodeDTO struct {
	ID         string         `json:"id"`
	Label      string         `json:"label"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
}

// GraphEdgeDTO matches Cognee's frontend expected format.
type GraphEdgeDTO struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Label  string `json:"label"`
}

// GraphDTO is the response format for graph visualization.
type GraphDTO struct {
	Nodes []GraphNodeDTO `json:"nodes"`
	Edges []GraphEdgeDTO `json:"edges"`
}

// DatasetGraph returns graph data for a dataset in Cognee-compatible format.
// GET /api/v1/datasets/:id/graph
func DatasetGraph(cfg GraphVisualizationConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.Neo4jURL == "" {
			return c.Status(503).JSON(fiber.Map{"error": "Neo4j not configured"})
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		writer, err := graphdb.NewWriter(ctx, cfg.Neo4jURL, cfg.Neo4jUser, cfg.Neo4jPassword, cfg.Neo4jDatabase)
		if err != nil {
			return c.Status(503).JSON(fiber.Map{"error": fmt.Sprintf("neo4j: %v", err)})
		}
		defer writer.Close(ctx)

		result, err := writer.ReadFullGraph(ctx)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": fmt.Sprintf("read graph: %v", err)})
		}

		dto := GraphDTO{
			Nodes: make([]GraphNodeDTO, len(result.Nodes)),
			Edges: make([]GraphEdgeDTO, len(result.Edges)),
		}

		for i, n := range result.Nodes {
			name := bestNodeLabel(n)
			dto.Nodes[i] = GraphNodeDTO{
				ID:         n.ID,
				Label:      name,
				Type:       n.Label,
				Properties: n.Properties,
			}
		}

		for i, e := range result.Edges {
			dto.Edges[i] = GraphEdgeDTO{
				Source: e.SourceID,
				Target: e.TargetID,
				Label:  e.RelationshipType,
			}
		}

		return c.JSON(dto)
	}
}

// VisualizeHTML returns a self-contained HTML page with D3.js graph visualization.
// GET /api/v1/visualize
func VisualizeHTML(cfg GraphVisualizationConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.Neo4jURL == "" {
			return c.Status(503).SendString("Neo4j not configured")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		writer, err := graphdb.NewWriter(ctx, cfg.Neo4jURL, cfg.Neo4jUser, cfg.Neo4jPassword, cfg.Neo4jDatabase)
		if err != nil {
			return c.Status(503).SendString(fmt.Sprintf("Neo4j: %v", err))
		}
		defer writer.Close(ctx)

		result, err := writer.ReadFullGraph(ctx)
		if err != nil {
			return c.Status(500).SendString(fmt.Sprintf("Read graph: %v", err))
		}

		html := generateVisualizationHTML(result)
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.SendString(html)
	}
}

func generateVisualizationHTML(result graphdb.GraphReadResult) string {
	// Build nodes JSON
	var nodesJSON strings.Builder
	nodesJSON.WriteString("[")
	for i, n := range result.Nodes {
		if i > 0 {
			nodesJSON.WriteString(",")
		}
		name := bestNodeLabel(n)
		nodeType := n.Label
		if nodeType == "" {
			if t, ok := n.Properties["type"].(string); ok && t != "" {
				nodeType = t
			} else {
				nodeType = "Node"
			}
		}
		fmt.Fprintf(&nodesJSON, `{"id":"%s","name":"%s","type":"%s"}`,
			escapeJS(n.ID), escapeJS(name), escapeJS(nodeType))
	}
	nodesJSON.WriteString("]")

	// Build edges JSON
	var edgesJSON strings.Builder
	edgesJSON.WriteString("[")
	for i, e := range result.Edges {
		if i > 0 {
			edgesJSON.WriteString(",")
		}
		fmt.Fprintf(&edgesJSON, `{"source":"%s","target":"%s","label":"%s"}`,
			escapeJS(e.SourceID), escapeJS(e.TargetID), escapeJS(e.RelationshipType))
	}
	edgesJSON.WriteString("]")

	return fmt.Sprintf(htmlTemplate,
		len(result.Nodes), len(result.Edges),
		nodesJSON.String(), edgesJSON.String())
}

// bestNodeLabel extracts the best human-readable label from a node.
func bestNodeLabel(n graphdb.ReadNode) string {
	// Try common property names in priority order
	for _, key := range []string{"name", "label", "title", "text", "description", "relationship_name"} {
		if v, ok := n.Properties[key].(string); ok && v != "" {
			if len(v) > 60 {
				return v[:57] + "..."
			}
			return v
		}
	}
	// Fallback: type + short ID
	if n.Label != "" {
		return n.Label + ":" + n.ID[:8]
	}
	return n.ID[:12]
}

func escapeJS(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// Adapted from json for properties
func propsToJSON(props map[string]any) string {
	b, _ := json.Marshal(props)
	return string(b)
}

const htmlTemplate = `<!DOCTYPE html>
<html><head>
<meta charset="utf-8">
<title>Cognevra Knowledge Graph (%d nodes, %d edges)</title>
<style>
  body { margin: 0; font-family: -apple-system, sans-serif; background: #0a0a0a; color: #eee; overflow: hidden; }
  #info { position: absolute; top: 16px; left: 16px; background: rgba(0,0,0,0.8); padding: 12px 16px; border-radius: 8px; font-size: 13px; z-index: 10; }
  #info h3 { margin: 0 0 8px 0; color: #7c3aed; }
  #selected { position: absolute; top: 16px; right: 16px; width: 300px; background: rgba(0,0,0,0.85); padding: 16px; border-radius: 8px; font-size: 12px; max-height: 80vh; overflow-y: auto; display: none; z-index: 10; }
  #selected h4 { margin: 0 0 8px; color: #7c3aed; }
  #selected .prop { margin: 4px 0; }
  #selected .key { color: #888; }
  svg { width: 100vw; height: 100vh; }
  .node { cursor: pointer; }
  .link { stroke: #333; stroke-opacity: 0.6; }
  .label { font-size: 10px; fill: #aaa; pointer-events: none; }
</style>
</head><body>
<div id="info">
  <h3>🧠 Cognevra Graph</h3>
  <div>Nodes: <b>%d</b> | Edges: <b>%d</b></div>
  <div style="margin-top:8px;font-size:11px;color:#666">Drag to pan, scroll to zoom, click nodes</div>
</div>
<div id="selected"></div>
<script src="https://d3js.org/d3.v7.min.js"></script>
<script>
const nodes = %s;
const links = %s;

const typeColors = {
  Entity: "#8b5cf6", DocumentChunk: "#22c55e", TextSummary: "#06b6d4",
  EntityType: "#f59e0b", Character: "#ec4899", Location: "#10b981",
  Chapter: "#6366f1", GoEntity: "#a855f7", Node: "#6b7280"
};
function getColor(type) { return typeColors[type] || "#" + ((Math.abs(hashStr(type)) & 0xFFFFFF).toString(16)).padStart(6,"0"); }
function hashStr(s) { let h=0; for(let i=0;i<s.length;i++) h=((h<<5)-h)+s.charCodeAt(i); return h; }

const width = window.innerWidth, height = window.innerHeight;
const svg = d3.select("body").append("svg").attr("viewBox", [0,0,width,height]);

const g = svg.append("g");
svg.call(d3.zoom().scaleExtent([0.1, 8]).on("zoom", e => g.attr("transform", e.transform)));

const simulation = d3.forceSimulation(nodes)
  .force("link", d3.forceLink(links).id(d=>d.id).distance(80))
  .force("charge", d3.forceManyBody().strength(-200))
  .force("center", d3.forceCenter(width/2, height/2))
  .force("collision", d3.forceCollide(15));

const link = g.append("g").selectAll("line").data(links).join("line")
  .attr("class","link").attr("stroke-width",1);

const node = g.append("g").selectAll("circle").data(nodes).join("circle")
  .attr("class","node").attr("r", d => 5 + Math.sqrt((links.filter(l=>l.source===d||l.target===d||l.source.id===d.id||l.target.id===d.id).length||1))*2)
  .attr("fill", d => getColor(d.type))
  .call(d3.drag().on("start",dragStart).on("drag",dragged).on("end",dragEnd))
  .on("click", (e,d) => {
    const sel = document.getElementById("selected");
    sel.style.display = "block";
    sel.innerHTML = "<h4>" + d.name + "</h4><div class='prop'><span class='key'>Type:</span> " + d.type +
      "</div><div class='prop'><span class='key'>ID:</span> " + d.id + "</div>" +
      "<div class='prop'><span class='key'>Connections:</span> " +
      links.filter(l=>l.source.id===d.id||l.target.id===d.id).length + "</div>";
  });

node.append("title").text(d => d.name + " (" + d.type + ")");

const label = g.append("g").selectAll("text").data(nodes).join("text")
  .attr("class","label").text(d => d.name.length>20 ? d.name.slice(0,20)+"…" : d.name)
  .attr("dx",8).attr("dy",3);

simulation.on("tick", () => {
  link.attr("x1",d=>d.source.x).attr("y1",d=>d.source.y).attr("x2",d=>d.target.x).attr("y2",d=>d.target.y);
  node.attr("cx",d=>d.x).attr("cy",d=>d.y);
  label.attr("x",d=>d.x).attr("y",d=>d.y);
});

function dragStart(e,d){if(!e.active)simulation.alphaTarget(0.3).restart();d.fx=d.x;d.fy=d.y;}
function dragged(e,d){d.fx=e.x;d.fy=e.y;}
function dragEnd(e,d){if(!e.active)simulation.alphaTarget(0);d.fx=null;d.fy=null;}
</script></body></html>`
