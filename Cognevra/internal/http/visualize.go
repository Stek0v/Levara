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

	nodeCount := fmt.Sprintf("%d", len(result.Nodes))
	edgeCount := fmt.Sprintf("%d", len(result.Edges))

	html := strings.ReplaceAll(htmlTemplate, "{{NODES_DATA}}", nodesJSON.String())
	html = strings.ReplaceAll(html, "{{EDGES_DATA}}", edgesJSON.String())
	html = strings.ReplaceAll(html, "{{NODE_COUNT}}", nodeCount)
	html = strings.ReplaceAll(html, "{{EDGE_COUNT}}", edgeCount)
	return html
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
<title>Cognevra Knowledge Graph</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:#0f0f14;color:#e2e8f0;overflow:hidden}
svg{width:100vw;height:100vh;display:block}
.link{stroke:#2d2d3d;stroke-opacity:0.5}
.link:hover{stroke:#7c3aed;stroke-opacity:1}
.node{cursor:pointer;stroke:#1a1a2e;stroke-width:1.5}
.node:hover{stroke:#fff;stroke-width:2}
.label{font-size:11px;fill:#94a3b8;pointer-events:none;font-weight:500}
#panel{position:fixed;top:20px;left:20px;background:rgba(15,15,20,0.92);border:1px solid #2d2d3d;border-radius:12px;padding:16px 20px;z-index:10;min-width:200px;backdrop-filter:blur(12px)}
#panel h2{font-size:16px;color:#a78bfa;margin-bottom:8px;font-weight:600}
#panel .stat{font-size:13px;color:#64748b;margin:2px 0}
#panel .stat b{color:#e2e8f0}
#detail{position:fixed;top:20px;right:20px;width:320px;max-height:80vh;background:rgba(15,15,20,0.95);border:1px solid #2d2d3d;border-radius:12px;padding:20px;z-index:10;display:none;overflow-y:auto;backdrop-filter:blur(12px)}
#detail h3{font-size:15px;color:#a78bfa;margin-bottom:12px;word-break:break-word}
#detail .row{display:flex;margin:6px 0;font-size:12px}
#detail .key{color:#64748b;min-width:80px;flex-shrink:0}
#detail .val{color:#e2e8f0;word-break:break-word}
#legend{position:fixed;bottom:20px;left:20px;background:rgba(15,15,20,0.9);border:1px solid #2d2d3d;border-radius:10px;padding:12px 16px;z-index:10;display:flex;gap:12px;flex-wrap:wrap;max-width:600px}
.leg-item{display:flex;align-items:center;gap:5px;font-size:11px;color:#94a3b8}
.leg-dot{width:10px;height:10px;border-radius:50%;flex-shrink:0}
#hint{position:fixed;bottom:20px;right:20px;font-size:11px;color:#475569;z-index:10}
</style>
</head><body>
<div id="panel">
  <h2>🧠 Cognevra Graph</h2>
  <div class="stat">Nodes: <b>{{NODE_COUNT}}</b></div>
  <div class="stat">Edges: <b>{{EDGE_COUNT}}</b></div>
</div>
<div id="detail"></div>
<div id="legend"></div>
<div id="hint">Drag to pan · Scroll to zoom · Click nodes</div>
<script src="https://d3js.org/d3.v7.min.js"></script>
<script>
const nodes = {{NODES_DATA}};
const links = {{EDGES_DATA}};

const TC = {
  Entity:"#8b5cf6",DocumentChunk:"#22c55e",TextSummary:"#06b6d4",
  EntityType:"#eab308",Character:"#ec4899",Location:"#10b981",
  Chapter:"#6366f1",GoEntity:"#a855f7",Database:"#f97316",
  Algorithm:"#14b8a6",Feature:"#f472b6",Technology:"#38bdf8",
  TextDocument:"#84cc16",Node:"#64748b"
};
function color(t){return TC[t]||"#"+((Math.abs(hash(t))&0xFFFFFF).toString(16)).padStart(6,"0")}
function hash(s){let h=0;for(let i=0;i<s.length;i++)h=((h<<5)-h)+s.charCodeAt(i);return h}

// Build legend
const types={};nodes.forEach(n=>{types[n.type]=(types[n.type]||0)+1});
const leg=document.getElementById("legend");
Object.entries(types).sort((a,b)=>b[1]-a[1]).forEach(([t,c])=>{
  const d=document.createElement("div");d.className="leg-item";
  d.innerHTML='<div class="leg-dot" style="background:'+color(t)+'"></div>'+t+' ('+c+')';
  leg.appendChild(d);
});

const W=window.innerWidth,H=window.innerHeight;
const svg=d3.select("body").append("svg").attr("viewBox",[0,0,W,H]);
const g=svg.append("g");
svg.call(d3.zoom().scaleExtent([0.05,10]).on("zoom",e=>g.attr("transform",e.transform)));

// Compute degree for sizing
const deg={};links.forEach(l=>{deg[l.source]=(deg[l.source]||0)+1;deg[l.target]=(deg[l.target]||0)+1});
const maxDeg=Math.max(...Object.values(deg),1);

const sim=d3.forceSimulation(nodes)
  .force("link",d3.forceLink(links).id(d=>d.id).distance(100).strength(0.3))
  .force("charge",d3.forceManyBody().strength(-300))
  .force("center",d3.forceCenter(W/2,H/2))
  .force("collision",d3.forceCollide().radius(d=>nodeR(d)+4));

function nodeR(d){return 4+Math.sqrt((deg[d.id]||1)/maxDeg)*14}

const link=g.append("g").selectAll("line").data(links).join("line")
  .attr("class","link").attr("stroke-width",d=>1);

const node=g.append("g").selectAll("circle").data(nodes).join("circle")
  .attr("class","node").attr("r",d=>nodeR(d)).attr("fill",d=>color(d.type))
  .call(d3.drag().on("start",(e,d)=>{if(!e.active)sim.alphaTarget(0.3).restart();d.fx=d.x;d.fy=d.y})
    .on("drag",(e,d)=>{d.fx=e.x;d.fy=e.y})
    .on("end",(e,d)=>{if(!e.active)sim.alphaTarget(0);d.fx=null;d.fy=null}))
  .on("click",(e,d)=>{
    const det=document.getElementById("detail");det.style.display="block";
    const conns=links.filter(l=>l.source.id===d.id||l.target.id===d.id);
    let h='<h3>'+d.name+'</h3>';
    h+='<div class="row"><div class="key">Type</div><div class="val" style="color:'+color(d.type)+'">'+d.type+'</div></div>';
    h+='<div class="row"><div class="key">ID</div><div class="val" style="font-size:10px;color:#475569">'+d.id+'</div></div>';
    h+='<div class="row"><div class="key">Connections</div><div class="val">'+conns.length+'</div></div>';
    if(conns.length>0){
      h+='<div style="margin-top:10px;font-size:11px;color:#64748b">Relationships:</div>';
      conns.slice(0,10).forEach(l=>{
        const other=l.source.id===d.id?l.target:l.source;
        h+='<div class="row"><div class="key" style="color:'+color(other.type)+'">'+l.label+'</div><div class="val">'+other.name+'</div></div>';
      });
      if(conns.length>10)h+='<div style="font-size:10px;color:#475569;margin-top:4px">...and '+(conns.length-10)+' more</div>';
    }
    det.innerHTML=h;
  });

node.append("title").text(d=>d.name+" ("+d.type+")");

const label=g.append("g").selectAll("text").data(nodes.filter(d=>(deg[d.id]||0)>=2||nodes.length<30))
  .join("text").attr("class","label")
  .text(d=>d.name.length>25?d.name.slice(0,22)+"…":d.name)
  .attr("dx",d=>nodeR(d)+4).attr("dy",3);

sim.on("tick",()=>{
  link.attr("x1",d=>d.source.x).attr("y1",d=>d.source.y).attr("x2",d=>d.target.x).attr("y2",d=>d.target.y);
  node.attr("cx",d=>d.x).attr("cy",d=>d.y);
  label.attr("x",d=>d.x).attr("y",d=>d.y);
});
</script></body></html>`
