package remote

import (
	"os/exec"
	"strings"
	"testing"
)

// Execute the shipped render functions against a minimal DOM. This checks
// client-local selection and caches, which server snapshot tests cannot cover.
func TestBrowserCloseReconcilesStaleViewsAndCaches(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	var functions []string
	for _, line := range strings.Split(indexHTML, "\n") {
		for _, name := range []string{"render", "agents", "closeCommand", "drawWaiting"} {
			if strings.HasPrefix(line, "function "+name+"(") {
				functions = append(functions, line)
			}
		}
	}
	if len(functions) != 4 {
		t.Fatal("browser render functions missing")
	}
	harness := `
const assert=require('assert').strict;
function element(){return {dataset:{},children:[],value:'',textContent:'',innerHTML:'',hidden:false,className:'',classList:{contains(){return false}},replaceChildren(...nodes){this.children=nodes;this.firstChild=nodes[0]},focus(){},scrollHeight:0,scrollTop:0,clientHeight:0}}
const tabs=element(),out=element(),status=element(),interaction=element(),command=element(),input=element(),send=element(),scroll=element(),queueSubmit=element(),waiting=element(),waitingText=element(),waitingCancel=element();
const document={createElement:element,getElementById(){return queueSubmit}};
let snapshot,active='agent-1',historyAtBeginning=false,promptNotice='',waitingSummary,waitingRunning=false,waitingQueued=0,waitingFrame=0;
const drafts={'main':'main draft','agent-1':'worker draft'},cleared={'agent-1':5},queueExpanded={'agent-1':true},queueScroll={'agent-1':10},spinnerFrames=['x'];
const ansiHTML=x=>x,escapeHTML=x=>x,waitingDetails=()=>'',renderLatestPrompt=()=>{},renderQueue=()=>{};
let shownInteraction;
function renderInteraction(value){shownInteraction=value}
` + strings.Join(functions, "\n") + `
const main={id:'main',name:'main',lines:['main transcript']},worker={id:'agent-1',name:'worker',status:'closing',lines:['worker transcript']};
snapshot={runtime:{agents:[{id:'main'},{id:'agent-1',status:'closing',active_task_id:'request-1'}],interactions:[]},presentation:{active:'agent-1',views:[main,worker]}};
render();
assert.equal(active,'agent-1');assert.equal(tabs.children.length,2);assert.match(tabs.children[1].textContent,/closing/);assert.match(waitingText.textContent,/Cancellation pending/);assert.equal(waitingCancel.hidden,true);
command.dataset.agent='agent-1';command.children=['pending dialog'];input.value='unsent worker text';
snapshot.runtime.agents=[{id:'main'}];snapshot.runtime.interactions=[];
render();
assert.equal(active,'main');assert.equal(input.value,'main draft');assert.equal(tabs.children.length,1);assert.equal(out.innerHTML,'main transcript');assert.equal(command.children.length,0);assert.equal(shownInteraction,undefined);
for(const cache of [drafts,cleared,queueExpanded,queueScroll])assert.equal('agent-1' in cache,false);
// Keep the saved selection on an initial connection when that agent is live.
active='';snapshot.runtime.agents=[{id:'main'},{id:'agent-2'}];snapshot.presentation.active='agent-2';snapshot.presentation.views=[main,{id:'agent-2',lines:[]}];render();assert.equal(active,'agent-2');
`
	if output, err := exec.Command(node, "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("browser close render: %v\n%s", err, output)
	}
}
