package main

import (
	"encoding/json"
	"net/http"
)

// productionViewerRoutes registers the production viewer on an existing mux.
func productionViewerRoutes(mux *http.ServeMux, infoFn func(channelID string) map[string]interface{}, channelsFn func() []viewerChannelRef, opts ...viewerRouteOptions) {
	registerViewerRoutes(mux, productionViewerHTML, infoFn, channelsFn, opts...)
}

// standalonePortalRoutes serves the production viewer with tune box for standalone portal.
func standalonePortalRoutes(mux *http.ServeMux, infoFn func(channelID string) map[string]interface{}, channelsFn func() []viewerChannelRef, opts ...viewerRouteOptions) {
	registerViewerRoutes(mux, portalViewerHTML, infoFn, channelsFn, opts...)
}

// registerViewerRoutes is the shared route registration for production and portal viewers.
func registerViewerRoutes(mux *http.ServeMux, html string, infoFn func(channelID string) map[string]interface{}, channelsFn func() []viewerChannelRef, opts ...viewerRouteOptions) {
	var opt viewerRouteOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if !opt.authenticate(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})
	mux.HandleFunc("GET /favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write([]byte(viewerFavicon))
	})
	mux.HandleFunc("GET /hls.min.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(hlsJSData)
	})
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		if !opt.authenticate(w, r) {
			return
		}
		reqChID := r.URL.Query().Get("channel")
		info := infoFn(reqChID)
		if channelsFn != nil {
			channels := channelsFn()
			if channels != nil {
				chList := make([]interface{}, len(channels))
				for i, ch := range channels {
					entry := map[string]interface{}{"id": ch.ID, "name": ch.Name}
					if ch.IconPath != "" {
						entry["icon_path"] = ch.IconPath
					}
					if ch.Guide != nil {
						var g map[string]interface{}
						if json.Unmarshal(ch.Guide, &g) == nil {
							entry["guide"] = g
						}
					}
					chList[i] = entry
				}
				info["channels"] = chList
			}
		}
		opt.applyDisplayConfig(info)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.Encode(info)
	})
}

// ========================================================================
// CSS
// ========================================================================

const productionCSS = `
@keyframes spin{to{transform:rotate(360deg)}}
.viewer{display:flex;flex-direction:column;align-items:center;padding:0 1rem;padding-top:1.25rem;min-height:calc(100vh - 45px)}
.player-block{width:100%;max-width:1100px;overflow:hidden;position:sticky;top:0;z-index:20;background:#000}
.player{position:relative;background:#000;aspect-ratio:16/9}
.player video{width:100%;height:100%;display:block;object-fit:contain}
.overlay{position:absolute;inset:0;background:rgba(0,0,0,.88);display:flex;flex-direction:column;align-items:center;justify-content:center;gap:.75rem;z-index:10}
.overlay.h{display:none}
.overlay-msg{font-size:.75rem;color:rgba(255,255,255,.5);text-align:center}
.spinner{width:14px;height:14px;border:1.5px solid rgba(255,255,255,.15);border-top-color:rgba(255,255,255,.5);border-radius:50%;animation:spin 1s linear infinite}
.controls-bar{display:flex;align-items:center;gap:.5rem;height:36px;padding:0}
.bar-name{font-size:.85rem;font-weight:600;color:#fff;white-space:nowrap;max-width:200px;overflow:hidden;text-overflow:ellipsis;flex-shrink:0}
.bar-sep{font-size:.8rem;color:#666}
.bar-program{font-size:.8rem;color:#999;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0;flex-shrink:1}
.bar-relay{font-size:.65rem;color:#666;border:1px solid #666;padding:1px 5px;white-space:nowrap;letter-spacing:.02em;flex-shrink:0}
.bar-relay-spoofed{color:#e8a735;border-color:#e8a735}
.bar-icon{height:20px;width:20px;border-radius:3px;flex-shrink:0}
.bar-spacer{flex:1}
.bar-btn{background:none;border:none;color:#fff;padding:4px;display:flex;align-items:center;cursor:pointer;opacity:.7;flex-shrink:0}
.bar-btn:hover{opacity:1}
.volume-slider{width:50px;height:2px;-webkit-appearance:none;appearance:none;background:#333;border:none;outline:none;cursor:pointer;padding:0}
.volume-slider::-webkit-slider-thumb{-webkit-appearance:none;width:8px;height:8px;background:#fff;cursor:pointer}
.volume-slider::-moz-range-thumb{width:8px;height:8px;background:#fff;border:none;cursor:pointer}
.volume-slider::-moz-range-track{background:#333;height:2px;border:none}
.channel-bar{width:100%;max-width:1100px;display:flex;align-items:center;gap:.5rem;padding:.6rem 0;font-size:.8rem}
.uri-btn{background:none;border:none;color:#666;font-size:.8rem;font-family:inherit;padding:0;cursor:pointer;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0}
.uri-btn:hover{color:#fff}
.guide{width:100%;max-width:1100px;border-top:1px solid #333;padding-top:.75rem}
.guide-inner{display:flex}
.guide-labels{flex-shrink:0;width:140px}
.guide-corner{height:24px;display:flex;align-items:center;font-size:.75rem;color:#666;font-variant-numeric:tabular-nums}
.guide-label{height:36px;display:flex;align-items:center;gap:6px;padding:0;border:none;background:none;color:#999;font-size:.8rem;font-family:inherit;text-align:left;cursor:pointer;width:100%;min-width:0}
.guide-label:hover{color:#fff}
.guide-label.active{color:#fff;font-weight:700}
.label-icon{height:16px;width:16px;border-radius:2px;flex-shrink:0}
.label-name{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.label-remove{display:none;margin-left:auto;color:#555;font-size:.9rem;padding:0 2px;flex-shrink:0;line-height:1}
.guide-label:hover .label-remove{display:flex;align-items:center}
.label-remove:hover{color:#ef4444}
.guide-viewport{flex:1;overflow-x:auto;overflow-y:hidden;min-width:0}
.guide-timeline{position:relative;min-height:100%}
.time-header{height:24px;position:relative}
.time-mark{position:absolute;top:0;height:100%;display:flex;align-items:center;padding-left:6px;font-size:.7rem;color:#666;border-left:1px solid #333;box-sizing:border-box;font-variant-numeric:tabular-nums}
.guide-row{position:relative;height:36px;border-top:1px solid #333}
.guide-cell{position:absolute;top:1px;height:calc(100% - 1px);display:flex;align-items:center;padding:0 6px;cursor:pointer;box-sizing:border-box;overflow:hidden;border-right:1px solid #222;border-left:1px solid #222}
.guide-cell.now{border-left-color:#fff}
.cell-title{font-size:.75rem;color:#666;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.now-line{position:absolute;top:0;bottom:0;width:1px;background:#4fc3f7;z-index:10;pointer-events:none}
.viewer-footer{width:100%;max-width:1100px;border-top:1px solid #333;margin-top:auto;padding:2.5rem 2rem 4rem;display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;font-size:.85rem;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Arial,sans-serif;color:#666}
.footer-left{display:flex;align-items:center;gap:1.5rem}
.footer-mark{display:flex;align-items:center;color:#666;opacity:.4;border-bottom:none;text-decoration:none}
.footer-mark:hover{opacity:.7}
.footer-mark svg{height:14px;width:auto}
.viewer-footer a{color:#666;text-decoration:none;border-bottom:1px solid #333}
.viewer-footer a:hover{color:#fff}
.tune-input{flex:1;background:none;border:none;color:#fff;font-size:.8rem;font-family:inherit;outline:none;padding:0}
.tune-input::placeholder{color:#666}
.text-btn{background:none;border:none;color:#fff;font-size:.8rem;font-family:inherit;cursor:pointer;padding:0}
.text-btn:hover{color:#999}
.tune-err{font-size:.75rem;color:#ef4444}
@media(max-width:640px){.viewer{padding:0 .75rem}.guide-labels{width:90px}.guide-label{height:30px;font-size:.6rem}.guide-row{height:30px}.guide-corner{height:20px;font-size:.55rem}.time-header{height:20px}.time-mark{font-size:.5rem}.controls-bar{height:30px}.bar-program{display:none}.bar-sep{display:none}.volume-slider{display:none}.viewer-footer{margin-top:1.5rem;padding:1.5rem 1rem 2rem;font-size:.75rem}}
`

// ========================================================================
// SVG Icons
// ========================================================================

const svgMute = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"></polygon><line x1="23" y1="9" x2="17" y2="15"></line><line x1="17" y1="9" x2="23" y2="15"></line></svg>`
const svgVolLow = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"></polygon><path d="M15.54 8.46a3.5 3.5 0 0 1 0 7.07"></path></svg>`
const svgVolHigh = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"></polygon><path d="M15.54 8.46a5 5 0 0 1 0 7.07"></path><path d="M19.07 4.93a10 10 0 0 1 0 14.14"></path></svg>`
const svgPiP = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="14"></rect><rect x="12" y="9" width="8" height="6" fill="currentColor" stroke="none" opacity="0.5"></rect></svg>`
const svgFullscreen = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="15 3 21 3 21 9"></polyline><polyline points="9 21 3 21 3 15"></polyline><line x1="21" y1="3" x2="14" y2="10"></line><line x1="3" y1="21" x2="10" y2="14"></line></svg>`
const svgFooterMark = `<svg viewBox="0 0 491.3 349.8" fill="currentColor" aria-hidden="true"><g transform="translate(-0.18,349.81) scale(0.1,-0.1)"><path d="M2050 3493 c-881 -31 -1492 -102 -1671 -194 -95 -48 -171 -145 -214 -272 -98 -292 -154 -705 -162 -1192 -8 -497 27 -842 127 -1233 48 -187 74 -246 140 -316 96 -102 195 -137 505 -181 756 -105 1734 -133 2665 -75 330 21 800 78 959 117 195 47 311 159 370 358 52 179 98 442 128 735 24 242 24 784 0 1030 -40 397 -116 746 -191 874 -34 58 -117 133 -179 161 -243 111 -1187 198 -2092 193 -170 -1 -344 -3 -385 -5z m-246 -993 c33 -5 92 -25 132 -44 68 -32 101 -64 609 -571 296 -295 547 -543 559 -550 17 -11 80 -15 299 -15 392 -2 361 -37 365 410 3 365 0 389 -57 427 -33 23 -39 23 -303 23 -177 0 -277 -4 -291 -11 -12 -6 -95 -84 -185 -172 l-162 -162 -113 113 -112 112 170 170 c178 178 221 211 320 250 57 22 75 24 326 28 170 2 293 0 340 -7 193 -30 343 -175 378 -365 14 -76 15 -704 1 -777 -15 -79 -67 -177 -124 -235 -58 -57 -156 -109 -235 -124 -70 -13 -552 -13 -622 0 -30 5 -85 26 -124 45 -64 31 -111 75 -580 545 -280 282 -532 529 -558 551 l-49 39 -278 0 -278 0 -30 -25 c-52 -43 -53 -53 -50 -424 l3 -343 37 -34 c22 -20 49 -35 65 -35 100 -6 532 5 548 13 11 6 92 82 180 169 l160 159 113 -113 112 -113 -183 -182 c-262 -260 -269 -262 -677 -262 -141 0 -281 5 -311 10 -170 32 -310 164 -355 335 -10 37 -14 143 -14 423 0 351 1 376 21 434 52 156 176 269 332 303 75 16 530 20 621 5z"/></g></svg>`

// ========================================================================
// Shared Player/Guide/Controls JavaScript (used by both prod and portal)
// ========================================================================

const viewerCoreJS = `
'use strict';
const V=document.getElementById('v');
const OV=document.getElementById('ov');
const OVMSG=OV.querySelector('.overlay-msg');
let _hls=null,_info=null,_stallTimer=null,_retryDelay=2000;
let _chID='';
// Token propagation for private embedded viewers (§11D)
var _viewerToken=(new URLSearchParams(window.location.search)).get('token')||'';
function withToken(u){
  if(!_viewerToken||!u||u.indexOf('token=')!==-1) return u;
  return u+(u.indexOf('?')!==-1?'&':'?')+'token='+encodeURIComponent(_viewerToken);
}
// Timer management — prevents accumulation on portal re-tune (§11E)
var _infoTimer=0,_guideTimer=0,_clockTimers=[];
function clearTimers(){if(_infoTimer)clearInterval(_infoTimer);if(_guideTimer)clearInterval(_guideTimer);_clockTimers.forEach(function(t){clearInterval(t)});_infoTimer=0;_guideTimer=0;_clockTimers=[]}
function startTimers(){clearTimers();_infoTimer=setInterval(refreshInfo,60000);_guideTimer=setInterval(refreshGuide,30000)}
// Portal saved channels (§8) — single-user, localStorage by default with
// optional file-backed persistence via /api/saved-channels on standalone viewer.
var _isPortal=false,_saved=[];
const PX_PER_MIN=4,SLOT_MIN=30;

function esc(s){return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;')}

// ---- Player ----
function startPlayer(src){
  if(!src)return;
  if(Hls.isSupported()){
    if(_hls){_hls.destroy();_hls=null}
    _hls=new Hls({enableWorker:true,lowLatencyMode:false});
    _hls.loadSource(src);_hls.attachMedia(V);
    _hls.on(Hls.Events.MANIFEST_PARSED,function(){V.play().catch(function(){});OV.classList.add('h');_retryDelay=2000;
      // Detect audio-only streams and show a non-error overlay
      var lv0=_hls.levels&&_hls.levels[0];
      if(lv0&&lv0.codecs&&!lv0.videoCodec&&(lv0.audioCodec||/mp4a|aac/i.test(lv0.codecs))){
        OV.classList.remove('h');OVMSG.innerHTML='<div style="text-align:center"><svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="rgba(255,255,255,.3)" stroke-width="1.5"><path d="M9 18V5l12-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="16" r="3"/></svg><div style="margin-top:8px;color:rgba(255,255,255,.4);font-size:.7rem">audio only</div></div>';
      }
    });
    _hls.on(Hls.Events.ERROR,function(_,d){if(d.fatal){if(d.type===Hls.ErrorTypes.MEDIA_ERROR){_hls.recoverMediaError()}else{scheduleRetry()}}});
    _hls.on(Hls.Events.AUDIO_TRACKS_UPDATED,function(){buildTrackSel('audio',_hls.audioTracks,_hls.audioTrack,function(i){_hls.audioTrack=i})});
    _hls.on(Hls.Events.SUBTITLE_TRACKS_UPDATED,function(){buildTrackSel('subs',_hls.subtitleTracks,_hls.subtitleTrack,function(i){_hls.subtitleTrack=i})});
  }else if(V.canPlayType('application/vnd.apple.mpegurl')){
    V.src=src;V.addEventListener('loadedmetadata',function(){V.play().catch(function(){});OV.classList.add('h')});
  }
}
function scheduleRetry(){
  OV.classList.remove('h');OVMSG.textContent='reconnecting...';
  setTimeout(function(){if(_info&&_info.stream_src)startPlayer(_info.stream_src);_retryDelay=Math.min(_retryDelay*2,30000)},_retryDelay);
}
function startStallDetection(){
  if(_stallTimer)return;var last=0;
  _stallTimer=setInterval(function(){if(V.paused||V.ended)return;if(V.currentTime===last&&last>0){if(_hls)_hls.recoverMediaError();else{V.load();V.play().catch(function(){})}}last=V.currentTime},8000);
  document.addEventListener('visibilitychange',function(){if(!document.hidden&&V.paused&&!V.ended)V.play().catch(function(){})});
}

// ---- Controls ----
function toggleMute(){V.muted=!V.muted;updateVolIcon()}
function setVol(val){V.volume=parseFloat(val);if(V.muted&&V.volume>0)V.muted=false;updateVolIcon()}
function updateVolIcon(){document.getElementById('mb').innerHTML=V.volume===0||V.muted?'` + svgMute + `':(V.volume>.5?'` + svgVolHigh + `':'` + svgVolLow + `')}
function toggleFS(){var c=document.querySelector('.player');if(document.fullscreenElement)document.exitFullscreen();else c.requestFullscreen().catch(function(){})}
function togglePiP(){if(document.pictureInPictureElement)document.exitPictureInPicture();else V.requestPictureInPicture().catch(function(){})}
function copyURI(){
  var el=document.getElementById('uri-display');
  if(!el||!el.textContent)return;
  function flash(){el.style.color='#999';setTimeout(function(){el.style.color=''},1500)}
  function fallback(){var ta=document.createElement('textarea');ta.value=el.textContent;ta.style.cssText='position:fixed;opacity:0';document.body.appendChild(ta);ta.select();try{document.execCommand('copy')}catch(e){}document.body.removeChild(ta);flash()}
  if(window.isSecureContext&&navigator.clipboard){navigator.clipboard.writeText(el.textContent).then(flash).catch(fallback)}else{fallback()}
}
window.toggleMute=toggleMute;window.setVol=setVol;window.toggleFS=toggleFS;window.togglePiP=togglePiP;window.copyURI=copyURI;
if(document.pictureInPictureEnabled){var pb=document.getElementById('pipb');if(pb)pb.style.display=''}

function buildTrackSel(type,tracks,cur,setter){
  var id='sel-'+type,sel=document.getElementById(id);
  if(tracks.length<=1){if(sel)sel.style.display='none';return}
  if(!sel){sel=document.createElement('select');sel.id=id;sel.style.cssText='background:none;border:1px solid #333;color:#666;font-family:inherit;font-size:.7rem;padding:2px 6px;cursor:pointer;flex-shrink:0';var sp=document.querySelector('.bar-spacer');if(sp)sp.insertAdjacentElement('afterend',sel)}
  sel.innerHTML='';
  if(type==='subs'){var o=document.createElement('option');o.value=-1;o.textContent='CC off';sel.appendChild(o)}
  tracks.forEach(function(tr,i){var o=document.createElement('option');o.value=i;o.textContent=tr.name||tr.lang||('Track '+(i+1));if(i===cur)o.selected=true;sel.appendChild(o)});
  sel.onchange=function(){setter(parseInt(sel.value))};sel.style.display='';
}

// ---- UI ----
function applyViewerConfig(d){
  if(d.viewer_title!==undefined){var hdl=document.querySelector('.hdl');if(hdl)hdl.textContent=d.viewer_title}
  if(d.viewer_footer===false){var ft=document.querySelector('.viewer-footer');if(ft)ft.style.display='none'}
}
function updateUI(){
  if(!_info||_info.portal)return;
  applyViewerConfig(_info);
  document.getElementById('cn').textContent=_info.channel_name||_info.channel_id||'';
  // Icon in controls bar
  var icon=document.getElementById('ch-icon');
  var meta=_info.metadata||{};
  if(icon){
    if(_info.icon_url){icon.src=withToken(_info.icon_url);icon.style.display=''}
    else if(meta.icon){var ext=meta.icon.split('.').pop()||'svg';icon.src=withToken('/tltv/v1/channels/'+_chID+'/icon.'+ext);icon.style.display=''}
    else{icon.style.display='none'}
  }
  // URI
  var tltvUri=_info.tltv_uri||('tltv://'+_chID+'@'+location.host);
  var uriEl=document.getElementById('uri-display');
  if(uriEl){uriEl.textContent=tltvUri;uriEl.title='Click to copy'}
  // Relay badge
  var rb=document.getElementById('rb');
  if(rb&&meta.origins&&Array.isArray(meta.origins)&&meta.origins.length>0){
    var connHost=location.port?location.hostname+':'+location.port:location.hostname;
    var isOrigin=meta.origins.some(function(o){var n=o.replace(/:443$/,'');return n===connHost||o===connHost+':443'||o===connHost});
    if(!isOrigin){rb.style.display='';rb.textContent='relay';rb.className='bar-relay'}else{rb.style.display='none'}
  }else if(rb){rb.style.display='none'}
  updateNowPlaying();
}
function updateNowPlaying(){
  var prg=document.getElementById('prg'),ps=document.getElementById('ps');
  var guide=_info&&_info.guide;
  if(!guide||!guide.entries){prg.textContent='';ps.style.display='none';return}
  var now=new Date(),found=null;
  for(var i=0;i<guide.entries.length;i++){var e=guide.entries[i],s=new Date(e.start),en=new Date(e.end);if(now>=s&&now<en){found=e;break}}
  if(found){prg.textContent=found.title||'';ps.style.display=''}else{prg.textContent='';ps.style.display='none'}
}

// ---- Guide ----
function buildGuide(){
  var guideEl=document.getElementById('guide');
  var labelsEl=document.getElementById('guide-labels');
  var timelineEl=document.getElementById('guide-timeline');
  if(!guideEl)return;
  var channels=_info&&_info.channels||[];
  if(channels.length===0&&_isPortal&&_saved.length>0)channels=_saved;
  var guide=_info&&_info.guide;
  var entries=(guide&&guide.entries)||[];
  if(channels.length===0&&entries.length===0){guideEl.style.display='none';return}
  var multiCh=channels.length>1||(_isPortal&&channels.length>0);
  guideEl.style.display='';
  var now=new Date();
  var startMin=Math.floor((now.getHours()*60+now.getMinutes())/SLOT_MIN)*SLOT_MIN;
  var dayStart=new Date(now);dayStart.setHours(0,0,0,0);
  var guideStart=new Date(dayStart.getTime()+startMin*60000);
  var totalMin=24*60,totalPx=totalMin*PX_PER_MIN,slotPx=SLOT_MIN*PX_PER_MIN;
  // Labels
  while(labelsEl.children.length>1)labelsEl.removeChild(labelsEl.lastChild);
  var meta=(_info&&_info.metadata)||{};
  function addLabel(ch,isActive){
    var btn=document.createElement('button');
    btn.className='guide-label'+(isActive?' active':'');
    var h='';
    // Icon — active channel uses metadata icon or icon_url; others use icon_path or cached data-URI
    if(isActive&&(_info.icon_url||meta.icon)){
      var iSrc=_info.icon_url||('/tltv/v1/channels/'+ch.id+'/icon.'+(meta.icon?meta.icon.split('.').pop():'svg'));
      h+='<img class="label-icon" src="'+esc(withToken(iSrc))+'" alt="" onerror="this.style.display=\'none\'">';
    }else if(ch.icon_path){
      h+='<img class="label-icon" src="'+esc(withToken(ch.icon_path))+'" alt="" onerror="this.style.display=\'none\'">';
    }else if(ch.icon_data&&ch.icon_data.indexOf('data:')===0){
      h+='<img class="label-icon" src="'+esc(ch.icon_data)+'" alt="" onerror="this.style.display=\'none\'">';
    }
    h+='<span class="label-name">'+esc(ch.name||ch.id)+'</span>';
    btn.innerHTML=h;
    if(_isPortal){
      var rm=document.createElement('span');rm.className='label-remove';rm.innerHTML='&times;';rm.title='Remove';
      rm.onclick=function(e){e.stopPropagation();removeSaved(ch.id)};btn.appendChild(rm);
    }
    if(multiCh){
      btn.onclick=(_isPortal&&ch.uri)?function(){if(ch.id===_chID)return;
        // Pre-update UI from saved data for instant visual feedback
        _chID=ch.id;
        document.getElementById('cn').textContent=ch.name||ch.id;
        var ic=document.getElementById('ch-icon');
        if(ic&&ch.icon_data&&ch.icon_data.indexOf('data:')===0){ic.src=ch.icon_data;ic.style.display='inline-block'}
        else if(ic){ic.style.display='none'}
        // Stop current playback
        clearTimers();if(_hls){_hls.destroy();_hls=null}V.removeAttribute('src');V.load();
        OV.classList.remove('h');OVMSG.textContent='connecting...';
        // Fast resolve (skip_discover) — populates srv with full metadata
        _skipDiscover=true;
        document.getElementById('tunein').value=ch.uri;
        tune();
      }:function(){switchChannel(ch.id)};
    }
    labelsEl.appendChild(btn);
  }
  if(multiCh){channels.forEach(function(ch){addLabel(ch,ch.id===_chID)})}
  else if(channels.length===1){addLabel(channels[0],true)}
  else{addLabel({id:_chID,name:_info.channel_name||_chID},true)}
  // Timeline
  var html='<div class="time-header" style="width:'+totalPx+'px">';
  for(var m=0;m<totalMin;m+=SLOT_MIN){
    var t=new Date(guideStart.getTime()+m*60000);
    html+='<div class="time-mark" style="left:'+(m*PX_PER_MIN)+'px;width:'+slotPx+'px">'+t.toLocaleTimeString([],{hour:'numeric',minute:'2-digit'})+'</div>';
  }
  html+='</div>';
  var nowMin=(now.getTime()-guideStart.getTime())/60000;
  function buildRow(rowEntries,chId){
    html+='<div class="guide-row" style="width:'+totalPx+'px">';
    for(var i=0;i<rowEntries.length;i++){
      var e=rowEntries[i],s=new Date(e.start),en=new Date(e.end);
      var sMin=Math.max(0,(s.getTime()-guideStart.getTime())/60000);
      var eMin=Math.min(totalMin,(en.getTime()-guideStart.getTime())/60000);
      if(eMin<=0||sMin>=totalMin)continue;
      var left=Math.round(sMin*PX_PER_MIN),width=Math.max(1,Math.round((eMin-sMin)*PX_PER_MIN));
      var isNow=(chId===_chID)&&now>=s&&now<en;
      var click=chId&&channels.length>1?' onclick="switchChannel(\''+esc(chId)+'\')"':'';
      html+='<div class="guide-cell'+(isNow?' now':'')+'" style="left:'+left+'px;width:'+width+'px" title="'+esc(e.title||'')+'"'+click+'>';
      if(width>50)html+='<span class="cell-title">'+esc(e.title||'')+'</span>';
      html+='</div>';
    }
    html+='</div>';
  }
  if(multiCh){
    channels.forEach(function(ch){
      if(ch.id===_chID){buildRow(entries,ch.id)}
      else{
        // Use per-channel guide data from the channels array when available
        var chGuide=ch.guide&&ch.guide.entries;
        if(chGuide&&chGuide.length>0){buildRow(chGuide,ch.id)}
        else{
          var rowClick=(_isPortal&&ch.uri)?'document.getElementById(\'tunein\').value=\''+esc(ch.uri)+'\';tune()':'switchChannel(\''+esc(ch.id)+'\')';
          html+='<div class="guide-row" style="width:'+totalPx+'px">';
          html+='<div class="guide-cell" style="left:0;width:'+totalPx+'px" onclick="'+rowClick+'">';
          html+='<span class="cell-title">'+esc(ch.name||ch.id)+'</span></div></div>';
        }
      }
    });
  }else{buildRow(entries,_chID)}
  if(nowMin>=0&&nowMin<totalMin)html+='<div class="now-line" style="left:'+(nowMin*PX_PER_MIN)+'px"></div>';
  timelineEl.style.width=totalPx+'px';timelineEl.innerHTML=html;
  var vp=document.getElementById('guide-viewport');
  if(vp&&nowMin>0)vp.scrollLeft=Math.max(0,nowMin*PX_PER_MIN-vp.clientWidth/3);
}

function switchChannel(id){
  if(id===_chID)return;
  fetch(withToken('/api/info?channel='+encodeURIComponent(id))).then(function(r){return r.json()}).then(function(d){
    _info=d;_chID=d.channel_id||id;updateUI();startPlayer(withToken(d.stream_src));buildGuide();
    var u=new URL(location);u.searchParams.set('channel',id);history.replaceState(null,'',u);
  }).catch(function(){});
}
window.switchChannel=switchChannel;

function startClock(){
  var el=document.getElementById('guide-clock');if(!el)return;
  _clockTimers.forEach(function(t){clearInterval(t)});_clockTimers=[];
  function tick(){el.textContent=new Date().toLocaleTimeString([],{hour:'numeric',minute:'2-digit',timeZoneName:'short'})}
  tick();_clockTimers.push(setInterval(tick,5000));
  _clockTimers.push(setInterval(function(){
    var line=document.querySelector('.now-line');if(!line||!_info)return;
    var now=new Date(),ds=new Date(now);
    var sm=Math.floor((ds.getHours()*60+ds.getMinutes())/SLOT_MIN)*SLOT_MIN;
    ds.setHours(0,0,0,0);
    var gs=new Date(ds.getTime()+sm*60000);
    line.style.left=((now.getTime()-gs.getTime())/60000*PX_PER_MIN)+'px';
    updateNowPlaying();
  },60000));
}

function refreshInfo(){
  if(!_chID)return;
  var url=withToken('/api/info?channel='+encodeURIComponent(_chID));
  fetch(url).then(function(r){return r.json()}).then(function(d){
    if(d.portal)return;var old=_info&&_info.stream_src;_info=d;
    if(d.stream_src&&d.stream_src!==old)startPlayer(withToken(d.stream_src));updateUI();
  }).catch(function(){});
}
function refreshGuide(){
  if(!_chID)return;
  var url=withToken('/api/info?channel='+encodeURIComponent(_chID));
  fetch(url).then(function(r){return r.json()}).then(function(d){
    if(d.portal)return;_info=d;buildGuide();updateNowPlaying();
  }).catch(function(){});
}
`

// ========================================================================
// Production Viewer HTML (embedded daemon mode — no tune box)
// ========================================================================

var productionViewerHTML = pageHead("tltv", productionCSS) + `
<body>
<div class="c">
` + pageNav("") + `
<div class="viewer">
  <div class="player-block">
    <div class="player">
      <video id="v" muted playsinline></video>
      <div id="ov" class="overlay"><div class="spinner"></div><div class="overlay-msg">connecting...</div></div>
    </div>
    <div class="controls-bar">
      <img id="ch-icon" class="bar-icon" style="display:none" alt="">
      <span id="cn" class="bar-name"></span>
      <span id="ps" class="bar-sep" style="display:none">/</span>
      <span id="prg" class="bar-program"></span>
      <span id="rb" class="bar-relay" style="display:none">relay</span>
      <span class="bar-spacer"></span>
      <button id="mb" class="bar-btn" onclick="toggleMute()" title="Mute/Unmute">` + svgMute + `</button>
      <input id="volr" class="volume-slider" type="range" min="0" max="1" step="0.05" value="1" oninput="setVol(this.value)">
      <button id="pipb" class="bar-btn" onclick="togglePiP()" style="display:none" title="Picture-in-Picture">` + svgPiP + `</button>
      <button class="bar-btn" onclick="toggleFS()" title="Fullscreen">` + svgFullscreen + `</button>
    </div>
  </div>
  <div class="channel-bar">
    <button id="uri-display" class="uri-btn" onclick="copyURI()" title="Click to copy"></button>
  </div>
  <div id="guide" class="guide" style="display:none">
    <div class="guide-inner">
      <div id="guide-labels" class="guide-labels"><div id="guide-clock" class="guide-corner"></div></div>
      <div id="guide-viewport" class="guide-viewport"><div id="guide-timeline" class="guide-timeline"></div></div>
    </div>
  </div>
  <footer class="viewer-footer">
    <div class="footer-left">
      <a href="https://timelooptv.org" class="footer-mark" aria-label="timelooptv.org">` + svgFooterMark + `</a>
      <a href="https://spec.timelooptv.org">spec</a>
      <a href="https://github.com/tltv-org">github</a>
    </div>
  </footer>
</div>
</div>
<script src="/hls.min.js"></script>
<script>
` + viewerCoreJS + `
// ---- Production Init ----
(function(){
  var params=new URLSearchParams(location.search);
  var reqCh=params.get('channel')||'';
  var url=reqCh?'/api/info?channel='+encodeURIComponent(reqCh):'/api/info';
  fetch(withToken(url)).then(function(r){return r.json()}).then(function(d){
    _info=d;_chID=d.channel_id||'';
    updateUI();startPlayer(withToken(d.stream_src));startStallDetection();buildGuide();startClock();
    startTimers();
  }).catch(function(e){OVMSG.textContent='failed to load channel info'});
})();
</script>
</body>
</html>`

// ========================================================================
// Portal Viewer HTML (standalone mode — tune box, no daemon channels)
// ========================================================================

var portalViewerHTML = pageHead("tltv", productionCSS) + `
<body>
<div class="c">
` + pageNav("viewer") + `
<div class="viewer">
  <div class="player-block">
    <div class="player">
      <video id="v" muted playsinline></video>
      <div id="ov" class="overlay"><div class="spinner"></div><div class="overlay-msg">enter a channel to tune</div></div>
    </div>
    <div class="controls-bar">
      <img id="ch-icon" class="bar-icon" style="display:none" alt="">
      <span id="cn" class="bar-name"></span>
      <span id="ps" class="bar-sep" style="display:none">/</span>
      <span id="prg" class="bar-program"></span>
      <span class="bar-spacer"></span>
      <button id="mb" class="bar-btn" onclick="toggleMute()" title="Mute/Unmute">` + svgMute + `</button>
      <input id="volr" class="volume-slider" type="range" min="0" max="1" step="0.05" value="1" oninput="setVol(this.value)">
      <button id="pipb" class="bar-btn" onclick="togglePiP()" style="display:none" title="Picture-in-Picture">` + svgPiP + `</button>
      <button class="bar-btn" onclick="toggleFS()" title="Fullscreen">` + svgFullscreen + `</button>
    </div>
  </div>
  <div class="channel-bar">
    <input id="tunein" class="tune-input" type="text" placeholder="hostname, id@host, or tltv:// URI" spellcheck="false" autocomplete="off"
           onkeydown="if(event.key==='Enter')tune();if(event.key==='Escape')clearTune()">
    <button class="text-btn" onclick="tune()">go</button>
    <button id="clearbtn" class="text-btn" style="display:none" onclick="clearTune()">clear</button>
  </div>
  <div id="guide" class="guide" style="display:none">
    <div class="guide-inner">
      <div id="guide-labels" class="guide-labels"><div id="guide-clock" class="guide-corner"></div></div>
      <div id="guide-viewport" class="guide-viewport"><div id="guide-timeline" class="guide-timeline"></div></div>
    </div>
  </div>
  <footer class="viewer-footer">
    <div class="footer-left">
      <a href="https://timelooptv.org" class="footer-mark" aria-label="timelooptv.org">` + svgFooterMark + `</a>
      <a href="https://spec.timelooptv.org">spec</a>
      <a href="https://github.com/tltv-org">github</a>
    </div>
  </footer>
</div>
</div>
<script src="/hls.min.js"></script>
<script>
` + viewerCoreJS + `
// ---- Portal Init ----
_isPortal=true;
var _savedServer=false;
function loadSavedLocal(){try{_saved=JSON.parse(localStorage.getItem('tltv_saved')||'[]')}catch(e){_saved=[]}}
function persistSaved(){
  try{localStorage.setItem('tltv_saved',JSON.stringify(_saved))}catch(e){}
  if(!_savedServer)return;
  fetch('/api/saved-channels',{
    method:'POST',
    headers:{'Content-Type':'application/json'},
    body:JSON.stringify({channels:_saved})
  }).then(function(r){return r.json()}).then(function(d){if(d&&d.enabled&&d.channels)_saved=d.channels}).catch(function(){});
}
function clearSavedLocal(){try{localStorage.removeItem('tltv_saved')}catch(e){}}
function loadSavedInitial(done){
  fetch('/api/saved-channels').then(function(r){return r.json()}).then(function(d){
    if(d&&d.enabled){_savedServer=true;_saved=d.channels||[]}
    else loadSavedLocal();
    done();
  }).catch(function(){loadSavedLocal();done()});
}
function removeSaved(id){
  _saved=_saved.filter(function(c){return c.id!==id});
  persistSaved();
  if(id===_chID){
    // Stop playback for the removed channel without clearing _saved
    clearTimers();
    if(_hls){_hls.destroy();_hls=null}
    V.removeAttribute('src');V.load();
    _info=null;_chID='';
    document.getElementById('cn').textContent='';
    document.getElementById('prg').textContent='';
    document.getElementById('ps').style.display='none';
    var icon=document.getElementById('ch-icon');if(icon)icon.style.display='none';
    var u=new URL(location);u.searchParams.delete('channel');history.replaceState(null,'',u);
    if(_saved.length>0){
      // Tune to the first remaining saved channel — skip discovery to avoid re-adding removed channels
      _skipDiscover=true;
      document.getElementById('tunein').value=_saved[0].uri||'';
      OV.classList.remove('h');OVMSG.textContent='switching...';
      buildGuide();startClock();
      if(_saved[0].uri){tune()}
    }else{
      document.getElementById('tunein').value='';
      document.getElementById('clearbtn').style.display='none';
      OV.classList.remove('h');OVMSG.textContent='enter a channel to tune';
      buildGuide();
    }
    return;
  }
  buildGuide();
}
window.removeSaved=removeSaved;
var _skipDiscover=false; // set by removeSaved to prevent re-adding removed channels
function saveTuned(d){
  // Save primary channel — no tokens persisted (§8)
  var _se={id:d.channel_id,name:d.channel_name||d.channel_id,uri:d.tltv_uri||''};
  if(d.guide)_se.guide={entries:(d.guide.entries||[]).slice(0,50)};
  if(d.icon_url){_se.icon_data='pending:'+d.icon_url}
  var _sf=false;for(var i=0;i<_saved.length;i++){if(_saved[i].id===_se.id){_saved[i]=_se;_sf=true;break}}
  if(!_sf)_saved.push(_se);
  // Also add any discovered sibling channels from the same host
  // (skip when re-tuning after remove to avoid re-adding removed channels)
  if(d.discovered_channels&&!_skipDiscover){
    d.discovered_channels.forEach(function(dc){
      if(dc.id===d.channel_id)return;
      var exists=false;
      for(var j=0;j<_saved.length;j++){if(_saved[j].id===dc.id){exists=true;_saved[j].name=dc.name||_saved[j].name;if(dc.guide)_saved[j].guide={entries:(dc.guide.entries||[]).slice(0,50)};if(dc.icon_data)_saved[j].icon_data=dc.icon_data;else if(dc.icon_url)_saved[j].icon_data='pending:'+dc.icon_url;break}}
      if(!exists){
        var se={id:dc.id,name:dc.name||dc.id,uri:dc.uri||''};
        if(dc.guide)se.guide={entries:(dc.guide.entries||[]).slice(0,50)};
        if(dc.icon_data)se.icon_data=dc.icon_data;
        _saved.push(se);
      }
    });
  }
  _skipDiscover=false;
  persistSaved();
  // Cache icon as data-URI for saved channel persistence
  if(d.icon_url){
    fetch(d.icon_url).then(function(r){return r.blob()}).then(function(blob){
      var reader=new FileReader();
      reader.onloadend=function(){
        for(var k=0;k<_saved.length;k++){if(_saved[k].id===d.channel_id){_saved[k].icon_data=reader.result;break}}
        persistSaved();
      };
      reader.readAsDataURL(blob);
    }).catch(function(){});
  }
}
function tune(){
  var input=document.getElementById('tunein');
  var target=input.value.trim();if(!target)return;
  OV.classList.remove('h');OVMSG.textContent='resolving...';
  var resolveUrl='/api/resolve?target='+encodeURIComponent(target);
  if(_skipDiscover)resolveUrl+='&skip_discover=1';
  fetch(resolveUrl).then(function(r){return r.json()}).then(function(d){
    if(d.error){
      OVMSG.innerHTML='<div style="text-align:center">tune failed<br><span class="tune-err" style="margin-top:4px;display:inline-block">'+esc(d.error)+'</span></div>';
      return;
    }
    _info=d;_chID=d.channel_id||'';
    saveTuned(d);
    updateUI();startPlayer(d.stream_src);startStallDetection();buildGuide();startClock();
    document.getElementById('clearbtn').style.display='';
    if(d.tltv_uri){
      input.value=d.tltv_uri;
      var u=new URL(location);u.searchParams.set('channel',d.tltv_uri);u.searchParams.delete('token');history.replaceState(null,'',u);
    }
    startTimers();
  }).catch(function(e){OVMSG.innerHTML='<div style="text-align:center">tune failed<br><span class="tune-err" style="margin-top:4px;display:inline-block">network error</span></div>'});
}
function clearTune(){
  clearTimers();
  if(_hls){_hls.destroy();_hls=null}
  V.removeAttribute('src');V.load();
  _info=null;_chID='';
  document.getElementById('tunein').value='';
  document.getElementById('cn').textContent='';
  document.getElementById('prg').textContent='';
  document.getElementById('ps').style.display='none';
  var icon=document.getElementById('ch-icon');if(icon)icon.style.display='none';
  document.getElementById('clearbtn').style.display='none';
  OV.classList.remove('h');OVMSG.textContent='enter a channel to tune';
  var u=new URL(location);u.searchParams.delete('channel');history.replaceState(null,'',u);
  // Clear saved channels and rebuild guide (hides when empty)
  _saved=[];clearSavedLocal();if(_savedServer)persistSaved();
  buildGuide();
}
window.tune=tune;window.clearTune=clearTune;

(function(){
  var params=new URLSearchParams(location.search);
  var chParam=params.get('channel')||'';
  loadSavedInitial(function(){
    // Try pre-tuned channel from server
    fetch('/api/info').then(function(r){return r.json()}).then(function(d){
      // Apply viewer display config (title/footer) even in portal mode
      applyViewerConfig(d);
      if(d&&!d.portal&&d.stream_src){
        _info=d;_chID=d.channel_id||'';
        updateUI();startPlayer(d.stream_src);startStallDetection();buildGuide();startClock();
        document.getElementById('clearbtn').style.display='';
        if(d.tltv_uri){document.getElementById('tunein').value=d.tltv_uri;var u=new URL(location);u.searchParams.set('channel',d.tltv_uri);u.searchParams.delete('token');history.replaceState(null,'',u)}
        startTimers();
      }else if(chParam){
        document.getElementById('tunein').value=chParam;tune();
      }else if(_saved.length>0){
        buildGuide();startClock();
      }
    }).catch(function(){
      if(chParam){document.getElementById('tunein').value=chParam;tune()}
      else if(_saved.length>0){buildGuide();startClock()}
    });
  });
})();
</script>
</body>
</html>`
