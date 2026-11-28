export namespace core {
	
	export class AddNodeGroupRequest {
	    groupName: string;
	    outboundLines: string;
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new AddNodeGroupRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.groupName = source["groupName"];
	        this.outboundLines = source["outboundLines"];
	        this.count = source["count"];
	    }
	}
	export class AddNodeRequest {
	    vpsAlias: string;
	    groupName: string;
	    inboundProtocol: string;
	    outboundProtocol: string;
	    startPort: number;
	    count: number;
	    tlsEnabled: boolean;
	    realityEnabled: boolean;
	    realitySni: string;
	    outboundLines: string;
	    outboundDetails: Record<string, any>;
	    rawConfigs: number[];
	
	    static createFrom(source: any = {}) {
	        return new AddNodeRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vpsAlias = source["vpsAlias"];
	        this.groupName = source["groupName"];
	        this.inboundProtocol = source["inboundProtocol"];
	        this.outboundProtocol = source["outboundProtocol"];
	        this.startPort = source["startPort"];
	        this.count = source["count"];
	        this.tlsEnabled = source["tlsEnabled"];
	        this.realityEnabled = source["realityEnabled"];
	        this.realitySni = source["realitySni"];
	        this.outboundLines = source["outboundLines"];
	        this.outboundDetails = source["outboundDetails"];
	        this.rawConfigs = source["rawConfigs"];
	    }
	}
	export class AppInfo {
	    name: string;
	    version: string;
	    dataDir: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	        this.dataDir = source["dataDir"];
	    }
	}
	export class HealthStatus {
	    id: number;
	    vps_alias: string;
	    vps_ip: string;
	    node_group: string;
	    node_tag: string;
	    filename: string;
	    inbound_port: number;
	    fail_count: number;
	    last_status: string;
	    last_message: string;
	    last_checked_at: string;
	
	    static createFrom(source: any = {}) {
	        return new HealthStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.vps_alias = source["vps_alias"];
	        this.vps_ip = source["vps_ip"];
	        this.node_group = source["node_group"];
	        this.node_tag = source["node_tag"];
	        this.filename = source["filename"];
	        this.inbound_port = source["inbound_port"];
	        this.fail_count = source["fail_count"];
	        this.last_status = source["last_status"];
	        this.last_message = source["last_message"];
	        this.last_checked_at = source["last_checked_at"];
	    }
	}
	export class AutoDeleteRunResult {
	    success: boolean;
	    message: string;
	    total: number;
	    failed: number;
	    deleted: number;
	    items: HealthStatus[];
	
	    static createFrom(source: any = {}) {
	        return new AutoDeleteRunResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	        this.total = source["total"];
	        this.failed = source["failed"];
	        this.deleted = source["deleted"];
	        this.items = this.convertValues(source["items"], HealthStatus);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AutoDeleteSettings {
	    enabled: boolean;
	    failure_days: number;
	    check_time: string;
	    timezone: string;
	    concurrency: number;
	
	    static createFrom(source: any = {}) {
	        return new AutoDeleteSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.failure_days = source["failure_days"];
	        this.check_time = source["check_time"];
	        this.timezone = source["timezone"];
	        this.concurrency = source["concurrency"];
	    }
	}
	export class BackupFile {
	    name: string;
	    path: string;
	    sourceAlias: string;
	    size: number;
	    modified: string;
	
	    static createFrom(source: any = {}) {
	        return new BackupFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.sourceAlias = source["sourceAlias"];
	        this.size = source["size"];
	        this.modified = source["modified"];
	    }
	}
	export class BatchAddNodeRequest {
	    vpsAlias: string;
	    inboundProtocol: string;
	    outboundProtocol: string;
	    startPort: number;
	    tlsEnabled: boolean;
	    realityEnabled: boolean;
	    realitySni: string;
	    groups: AddNodeGroupRequest[];
	
	    static createFrom(source: any = {}) {
	        return new BatchAddNodeRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vpsAlias = source["vpsAlias"];
	        this.inboundProtocol = source["inboundProtocol"];
	        this.outboundProtocol = source["outboundProtocol"];
	        this.startPort = source["startPort"];
	        this.tlsEnabled = source["tlsEnabled"];
	        this.realityEnabled = source["realityEnabled"];
	        this.realitySni = source["realitySni"];
	        this.groups = this.convertValues(source["groups"], AddNodeGroupRequest);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CheckResult {
	    success: boolean;
	    message: string;
	    latency: number;
	
	    static createFrom(source: any = {}) {
	        return new CheckResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	        this.latency = source["latency"];
	    }
	}
	export class CommandResult {
	    success: boolean;
	    stdout: string;
	    stderr: string;
	    exitCode: number;
	    message: string;
	    title?: string;
	    summary?: string;
	    analysis?: string;
	    output?: string;
	    install_message?: string;
	
	    static createFrom(source: any = {}) {
	        return new CommandResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.stdout = source["stdout"];
	        this.stderr = source["stderr"];
	        this.exitCode = source["exitCode"];
	        this.message = source["message"];
	        this.title = source["title"];
	        this.summary = source["summary"];
	        this.analysis = source["analysis"];
	        this.output = source["output"];
	        this.install_message = source["install_message"];
	    }
	}
	export class DeletedRecord {
	    id: number;
	    vps_alias: string;
	    vps_ip: string;
	    node_group: string;
	    node_tag: string;
	    filename: string;
	    inbound_port: number;
	    config_json: Record<string, any>;
	    deleted_at: string;
	
	    static createFrom(source: any = {}) {
	        return new DeletedRecord(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.vps_alias = source["vps_alias"];
	        this.vps_ip = source["vps_ip"];
	        this.node_group = source["node_group"];
	        this.node_tag = source["node_tag"];
	        this.filename = source["filename"];
	        this.inbound_port = source["inbound_port"];
	        this.config_json = source["config_json"];
	        this.deleted_at = source["deleted_at"];
	    }
	}
	export class ExportFile {
	    fileName: string;
	    mimeType: string;
	    base64: string;
	
	    static createFrom(source: any = {}) {
	        return new ExportFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fileName = source["fileName"];
	        this.mimeType = source["mimeType"];
	        this.base64 = source["base64"];
	    }
	}
	export class SelectedNodeExportTarget {
	    vpsAlias: string;
	    fileName: string;
	
	    static createFrom(source: any = {}) {
	        return new SelectedNodeExportTarget(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vpsAlias = source["vpsAlias"];
	        this.fileName = source["fileName"];
	    }
	}
	export class ExportSelectedNodesRequest {
	    nodes: SelectedNodeExportTarget[];
	
	    static createFrom(source: any = {}) {
	        return new ExportSelectedNodesRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.nodes = this.convertValues(source["nodes"], SelectedNodeExportTarget);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class ImportResult {
	    success: boolean;
	    message: string;
	    imported: number;
	    skipped: number;
	    errors: string[];
	
	    static createFrom(source: any = {}) {
	        return new ImportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	        this.imported = source["imported"];
	        this.skipped = source["skipped"];
	        this.errors = source["errors"];
	    }
	}
	export class MiddleAccelerationExcelRequest {
	    relayUrl: string;
	    username: string;
	    password: string;
	    entryId: string;
	    middleDomain: string;
	    fileName: string;
	    fileBase64: string;
	    maxRetry: number;
	    concurrency: number;
	    reuseExisting: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MiddleAccelerationExcelRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.relayUrl = source["relayUrl"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.entryId = source["entryId"];
	        this.middleDomain = source["middleDomain"];
	        this.fileName = source["fileName"];
	        this.fileBase64 = source["fileBase64"];
	        this.maxRetry = source["maxRetry"];
	        this.concurrency = source["concurrency"];
	        this.reuseExisting = source["reuseExisting"];
	    }
	}
	export class MiddleAccelerationExcelResult {
	    success: boolean;
	    message: string;
	    logs: string[];
	    output: string;
	    file: ExportFile;
	
	    static createFrom(source: any = {}) {
	        return new MiddleAccelerationExcelResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	        this.logs = source["logs"];
	        this.output = source["output"];
	        this.file = this.convertValues(source["file"], ExportFile);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class MiddleAccelerationExcelSavedResult {
	    success: boolean;
	    message: string;
	    logs: string[];
	    output: string;
	    filePath: string;
	    fileName: string;
	
	    static createFrom(source: any = {}) {
	        return new MiddleAccelerationExcelSavedResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	        this.logs = source["logs"];
	        this.output = source["output"];
	        this.filePath = source["filePath"];
	        this.fileName = source["fileName"];
	    }
	}
	export class MiddleAccelerationItem {
	    name: string;
	    protocol: string;
	    targetHost: string;
	    targetPort: number;
	    listenPort: number;
	    output: string;
	    success: boolean;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new MiddleAccelerationItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.protocol = source["protocol"];
	        this.targetHost = source["targetHost"];
	        this.targetPort = source["targetPort"];
	        this.listenPort = source["listenPort"];
	        this.output = source["output"];
	        this.success = source["success"];
	        this.message = source["message"];
	    }
	}
	export class MiddleAccelerationRequest {
	    relayUrl: string;
	    username: string;
	    password: string;
	    entryId: string;
	    middleDomain: string;
	    inputText: string;
	    maxRetry: number;
	    concurrency: number;
	    reuseExisting: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MiddleAccelerationRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.relayUrl = source["relayUrl"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.entryId = source["entryId"];
	        this.middleDomain = source["middleDomain"];
	        this.inputText = source["inputText"];
	        this.maxRetry = source["maxRetry"];
	        this.concurrency = source["concurrency"];
	        this.reuseExisting = source["reuseExisting"];
	    }
	}
	export class MiddleAccelerationResult {
	    success: boolean;
	    message: string;
	    output: string;
	    logs: string[];
	    items: MiddleAccelerationItem[];
	
	    static createFrom(source: any = {}) {
	        return new MiddleAccelerationResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	        this.output = source["output"];
	        this.logs = source["logs"];
	        this.items = this.convertValues(source["items"], MiddleAccelerationItem);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class MiddleAccountHistory {
	    username: string;
	    password: string;
	
	    static createFrom(source: any = {}) {
	        return new MiddleAccountHistory(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.username = source["username"];
	        this.password = source["password"];
	    }
	}
	export class MiddleSettings {
	    relayUrl: string;
	    username: string;
	    password: string;
	    entryId: string;
	    middleDomain: string;
	
	    static createFrom(source: any = {}) {
	        return new MiddleSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.relayUrl = source["relayUrl"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.entryId = source["entryId"];
	        this.middleDomain = source["middleDomain"];
	    }
	}
	export class MiddleSettingsHistory {
	    current: MiddleSettings;
	    relayUrls: string[];
	    accounts: MiddleAccountHistory[];
	    entryIds: string[];
	    middleDomains: string[];
	
	    static createFrom(source: any = {}) {
	        return new MiddleSettingsHistory(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.current = this.convertValues(source["current"], MiddleSettings);
	        this.relayUrls = source["relayUrls"];
	        this.accounts = this.convertValues(source["accounts"], MiddleAccountHistory);
	        this.entryIds = source["entryIds"];
	        this.middleDomains = source["middleDomains"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class MultiIPNodeRequest {
	    vpsAlias: string;
	    groupName: string;
	    inboundProtocol: string;
	    startPort: number;
	    realitySni: string;
	    ips: string[];
	
	    static createFrom(source: any = {}) {
	        return new MultiIPNodeRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vpsAlias = source["vpsAlias"];
	        this.groupName = source["groupName"];
	        this.inboundProtocol = source["inboundProtocol"];
	        this.startPort = source["startPort"];
	        this.realitySni = source["realitySni"];
	        this.ips = source["ips"];
	    }
	}
	export class NodeConnectionRecord {
	    id: number;
	    ip: string;
	    port: number;
	    username: string;
	    password: string;
	    renewal_date: string;
	    remark: string;
	    country: string;
	    connection_str: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new NodeConnectionRecord(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.ip = source["ip"];
	        this.port = source["port"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.renewal_date = source["renewal_date"];
	        this.remark = source["remark"];
	        this.country = source["country"];
	        this.connection_str = source["connection_str"];
	        this.created_at = source["created_at"];
	    }
	}
	export class NodeDefaults {
	    default_start_port: number;
	    used_ports: number[];
	    used_port_count: number;
	
	    static createFrom(source: any = {}) {
	        return new NodeDefaults(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.default_start_port = source["default_start_port"];
	        this.used_ports = source["used_ports"];
	        this.used_port_count = source["used_port_count"];
	    }
	}
	export class NodeRecord {
	    id: number;
	    country: string;
	    quantity: number;
	    created_date: string;
	    renewal_date: string;
	    remark: string;
	    created_at: string;
	    updated_at: string;
	
	    static createFrom(source: any = {}) {
	        return new NodeRecord(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.country = source["country"];
	        this.quantity = source["quantity"];
	        this.created_date = source["created_date"];
	        this.renewal_date = source["renewal_date"];
	        this.remark = source["remark"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class NodeSummary {
	    vpsAlias: string;
	    fileName: string;
	    tag: string;
	    groupName: string;
	    type: string;
	    host: string;
	    listenPort: number;
	    outbound: string;
	    outboundIp: string;
	    outboundPort: number;
	    serverName: string;
	    uuid: string;
	    username: string;
	    password: string;
	    flow: string;
	    reality: boolean;
	    publicKey: string;
	    shortId: string;
	    fingerprint: string;
	    createdAt: string;
	    sortAt: string;
	
	    static createFrom(source: any = {}) {
	        return new NodeSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vpsAlias = source["vpsAlias"];
	        this.fileName = source["fileName"];
	        this.tag = source["tag"];
	        this.groupName = source["groupName"];
	        this.type = source["type"];
	        this.host = source["host"];
	        this.listenPort = source["listenPort"];
	        this.outbound = source["outbound"];
	        this.outboundIp = source["outboundIp"];
	        this.outboundPort = source["outboundPort"];
	        this.serverName = source["serverName"];
	        this.uuid = source["uuid"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.flow = source["flow"];
	        this.reality = source["reality"];
	        this.publicKey = source["publicKey"];
	        this.shortId = source["shortId"];
	        this.fingerprint = source["fingerprint"];
	        this.createdAt = source["createdAt"];
	        this.sortAt = source["sortAt"];
	    }
	}
	export class OperationResult {
	    success: boolean;
	    message: string;
	    count: number;
	    items: string[];
	
	    static createFrom(source: any = {}) {
	        return new OperationResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	        this.count = source["count"];
	        this.items = source["items"];
	    }
	}
	export class OutboundIPResult {
	    vps_alias: string;
	    node_tag: string;
	    filename: string;
	    outbound_ip: string;
	    success: boolean;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new OutboundIPResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vps_alias = source["vps_alias"];
	        this.node_tag = source["node_tag"];
	        this.filename = source["filename"];
	        this.outbound_ip = source["outbound_ip"];
	        this.success = source["success"];
	        this.message = source["message"];
	    }
	}
	export class OverviewStat {
	    vpsCount: number;
	    nodeCount: number;
	    backupCount: number;
	
	    static createFrom(source: any = {}) {
	        return new OverviewStat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vpsCount = source["vpsCount"];
	        this.nodeCount = source["nodeCount"];
	        this.backupCount = source["backupCount"];
	    }
	}
	export class VPS {
	    id: string;
	    hostId?: string;
	    original_alias?: string;
	    address: string;
	    port: number;
	    username: string;
	    password?: string;
	    key_path: string;
	    domain: string;
	    middle_domain: string;
	    alias: string;
	    proxy_host: string;
	    proxy_port: any;
	    proxy_username: string;
	    proxy_password?: string;
	    global_middle_proxy: any;
	    middle_proxy_countries: any;
	    nodeCount: number;
	    startPort: number;
	    countryCode: string;
	    singboxStatus: string;
	
	    static createFrom(source: any = {}) {
	        return new VPS(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.hostId = source["hostId"];
	        this.original_alias = source["original_alias"];
	        this.address = source["address"];
	        this.port = source["port"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.key_path = source["key_path"];
	        this.domain = source["domain"];
	        this.middle_domain = source["middle_domain"];
	        this.alias = source["alias"];
	        this.proxy_host = source["proxy_host"];
	        this.proxy_port = source["proxy_port"];
	        this.proxy_username = source["proxy_username"];
	        this.proxy_password = source["proxy_password"];
	        this.global_middle_proxy = source["global_middle_proxy"];
	        this.middle_proxy_countries = source["middle_proxy_countries"];
	        this.nodeCount = source["nodeCount"];
	        this.startPort = source["startPort"];
	        this.countryCode = source["countryCode"];
	        this.singboxStatus = source["singboxStatus"];
	    }
	}
	export class Overview {
	    appInfo: AppInfo;
	    vpsList: VPS[];
	    stats: OverviewStat;
	
	    static createFrom(source: any = {}) {
	        return new Overview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.appInfo = this.convertValues(source["appInfo"], AppInfo);
	        this.vpsList = this.convertValues(source["vpsList"], VPS);
	        this.stats = this.convertValues(source["stats"], OverviewStat);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class ProxyDetectRequest {
	    target: string;
	    nodes: string;
	    concurrency: number;
	    testUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxyDetectRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.target = source["target"];
	        this.nodes = source["nodes"];
	        this.concurrency = source["concurrency"];
	        this.testUrl = source["testUrl"];
	    }
	}
	export class ProxyDetectResult {
	    line: string;
	    protocol: string;
	    host: string;
	    port: number;
	    country: string;
	    isp: string;
	    success: boolean;
	    latency: number;
	    tcpLatency: number;
	    urlLatency: number;
	    message: string;
	    detectTarget: string;
	    detectLocation: string;
	    detectExitIp: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxyDetectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.line = source["line"];
	        this.protocol = source["protocol"];
	        this.host = source["host"];
	        this.port = source["port"];
	        this.country = source["country"];
	        this.isp = source["isp"];
	        this.success = source["success"];
	        this.latency = source["latency"];
	        this.tcpLatency = source["tcpLatency"];
	        this.urlLatency = source["urlLatency"];
	        this.message = source["message"];
	        this.detectTarget = source["detectTarget"];
	        this.detectLocation = source["detectLocation"];
	        this.detectExitIp = source["detectExitIp"];
	    }
	}
	export class QRCodeResult {
	    link: string;
	    image: string;
	
	    static createFrom(source: any = {}) {
	        return new QRCodeResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.link = source["link"];
	        this.image = source["image"];
	    }
	}
	export class RuleGroup {
	    id: number;
	    name: string;
	    description: string;
	    rules: string[];
	    builtin: boolean;
	    created_at: string;
	    updated_at: string;
	
	    static createFrom(source: any = {}) {
	        return new RuleGroup(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.rules = source["rules"];
	        this.builtin = source["builtin"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	
	
	export class VPSIPList {
	    alias: string;
	    ips: string[];
	    count: number;
	    total_count: number;
	    created_ip_count: number;
	    default_start_port: number;
	
	    static createFrom(source: any = {}) {
	        return new VPSIPList(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.alias = source["alias"];
	        this.ips = source["ips"];
	        this.count = source["count"];
	        this.total_count = source["total_count"];
	        this.created_ip_count = source["created_ip_count"];
	        this.default_start_port = source["default_start_port"];
	    }
	}
	export class VPSRuntimeState {
	    alias: string;
	    status: string;
	    nodeCount: number;
	    startPort: number;
	    synced: boolean;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new VPSRuntimeState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.alias = source["alias"];
	        this.status = source["status"];
	        this.nodeCount = source["nodeCount"];
	        this.startPort = source["startPort"];
	        this.synced = source["synced"];
	        this.message = source["message"];
	    }
	}

}

export namespace main {
	
	export class AIConfig {
	    id: string;
	    name: string;
	    endpoint: string;
	    apiKey: string;
	    model: string;
	    models?: string[];
	    mode: string;
	
	    static createFrom(source: any = {}) {
	        return new AIConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.endpoint = source["endpoint"];
	        this.apiKey = source["apiKey"];
	        this.model = source["model"];
	        this.models = source["models"];
	        this.mode = source["mode"];
	    }
	}
	export class AppInfo {
	    name: string;
	    version: string;
	    platform: string;
	    arch: string;
	    goVersion: string;
	    dataDir: string;
	    logDir: string;
	    framework: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	        this.platform = source["platform"];
	        this.arch = source["arch"];
	        this.goVersion = source["goVersion"];
	        this.dataDir = source["dataDir"];
	        this.logDir = source["logDir"];
	        this.framework = source["framework"];
	    }
	}
	export class AppUpdateInfo {
	    available: boolean;
	    currentVersion: string;
	    latestVersion: string;
	    releaseNotes: string;
	    downloadUrl: string;
	    assetName?: string;
	    assetSize: number;
	    publishedAt: string;
	    status: string;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new AppUpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.currentVersion = source["currentVersion"];
	        this.latestVersion = source["latestVersion"];
	        this.releaseNotes = source["releaseNotes"];
	        this.downloadUrl = source["downloadUrl"];
	        this.assetName = source["assetName"];
	        this.assetSize = source["assetSize"];
	        this.publishedAt = source["publishedAt"];
	        this.status = source["status"];
	        this.message = source["message"];
	    }
	}
	export class Credential {
	    id: string;
	    name: string;
	    type: string;
	    password: string;
	    keyContent: string;
	    keyPassphrase: string;
	    keyFileName: string;
	    keyType: string;
	    keyFingerprint: string;
	    remark: string;
	
	    static createFrom(source: any = {}) {
	        return new Credential(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.type = source["type"];
	        this.password = source["password"];
	        this.keyContent = source["keyContent"];
	        this.keyPassphrase = source["keyPassphrase"];
	        this.keyFileName = source["keyFileName"];
	        this.keyType = source["keyType"];
	        this.keyFingerprint = source["keyFingerprint"];
	        this.remark = source["remark"];
	    }
	}
	export class GlobalSettings {
	    theme: string;
	    downloadDirectory: string;
	    aiPresetPrompt: string;
	    autoStart: boolean;
	    lockOnStartup: boolean;
	    protectExport: boolean;
	    currentPassword: string;
	    newPassword: string;
	    confirmPassword: string;
	    removeSecurity: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GlobalSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = source["theme"];
	        this.downloadDirectory = source["downloadDirectory"];
	        this.aiPresetPrompt = source["aiPresetPrompt"];
	        this.autoStart = source["autoStart"];
	        this.lockOnStartup = source["lockOnStartup"];
	        this.protectExport = source["protectExport"];
	        this.currentPassword = source["currentPassword"];
	        this.newPassword = source["newPassword"];
	        this.confirmPassword = source["confirmPassword"];
	        this.removeSecurity = source["removeSecurity"];
	    }
	}
	export class QuickCommand {
	    id: string;
	    name: string;
	    command: string;
	    type?: string;
	    timeoutSeconds?: number;
	    retries?: number;
	
	    static createFrom(source: any = {}) {
	        return new QuickCommand(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.command = source["command"];
	        this.type = source["type"];
	        this.timeoutSeconds = source["timeoutSeconds"];
	        this.retries = source["retries"];
	    }
	}
	export class Settings {
	    hosts: remote.Host[];
	    ai?: AIConfig;
	    aiProviders: AIConfig[];
	    currentProviderId: string;
	    quickCommands: QuickCommand[];
	    groups: string[];
	    credentials: Credential[];
	    theme: string;
	    downloadDirectory: string;
	    aiPresetPrompt: string;
	    autoStart: boolean;
	    securityHash?: string;
	    securitySalt?: string;
	    lockOnStartup: boolean;
	    protectExport: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hosts = this.convertValues(source["hosts"], remote.Host);
	        this.ai = this.convertValues(source["ai"], AIConfig);
	        this.aiProviders = this.convertValues(source["aiProviders"], AIConfig);
	        this.currentProviderId = source["currentProviderId"];
	        this.quickCommands = this.convertValues(source["quickCommands"], QuickCommand);
	        this.groups = source["groups"];
	        this.credentials = this.convertValues(source["credentials"], Credential);
	        this.theme = source["theme"];
	        this.downloadDirectory = source["downloadDirectory"];
	        this.aiPresetPrompt = source["aiPresetPrompt"];
	        this.autoStart = source["autoStart"];
	        this.securityHash = source["securityHash"];
	        this.securitySalt = source["securitySalt"];
	        this.lockOnStartup = source["lockOnStartup"];
	        this.protectExport = source["protectExport"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class HostImportResult {
	    settings: Settings;
	    imported: number;
	    skipped: number;
	    ignoredFiles: number;
	    finalShell: number;
	
	    static createFrom(source: any = {}) {
	        return new HostImportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.settings = this.convertValues(source["settings"], Settings);
	        this.imported = source["imported"];
	        this.skipped = source["skipped"];
	        this.ignoredFiles = source["ignoredFiles"];
	        this.finalShell = source["finalShell"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PortForwardInfo {
	    id: string;
	    hostId: string;
	    kind: string;
	    state: string;
	    listenAddress: string;
	    targetAddress?: string;
	    lastError?: string;
	    createdAt: string;
	
	    static createFrom(source: any = {}) {
	        return new PortForwardInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.hostId = source["hostId"];
	        this.kind = source["kind"];
	        this.state = source["state"];
	        this.listenAddress = source["listenAddress"];
	        this.targetAddress = source["targetAddress"];
	        this.lastError = source["lastError"];
	        this.createdAt = source["createdAt"];
	    }
	}
	export class PortForwardRequest {
	    hostId: string;
	    kind: string;
	    listenHost: string;
	    listenPort: number;
	    targetHost: string;
	    targetPort: number;
	    allowExternal: boolean;
	    connectTimeoutSeconds: number;
	    handshakeTimeoutSeconds: number;
	
	    static createFrom(source: any = {}) {
	        return new PortForwardRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.kind = source["kind"];
	        this.listenHost = source["listenHost"];
	        this.listenPort = source["listenPort"];
	        this.targetHost = source["targetHost"];
	        this.targetPort = source["targetPort"];
	        this.allowExternal = source["allowExternal"];
	        this.connectTimeoutSeconds = source["connectTimeoutSeconds"];
	        this.handshakeTimeoutSeconds = source["handshakeTimeoutSeconds"];
	    }
	}
	export class PrivateKeyImport {
	    fileName: string;
	    content: string;
	    encrypted: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PrivateKeyImport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fileName = source["fileName"];
	        this.content = source["content"];
	        this.encrypted = source["encrypted"];
	    }
	}
	export class PublicSettings {
	    theme: string;
	    downloadDirectory: string;
	    aiPresetPrompt: string;
	    autoStart: boolean;
	    lockOnStartup: boolean;
	    protectExport: boolean;
	    securityConfigured: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PublicSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = source["theme"];
	        this.downloadDirectory = source["downloadDirectory"];
	        this.aiPresetPrompt = source["aiPresetPrompt"];
	        this.autoStart = source["autoStart"];
	        this.lockOnStartup = source["lockOnStartup"];
	        this.protectExport = source["protectExport"];
	        this.securityConfigured = source["securityConfigured"];
	    }
	}
	
	export class SSHSessionLogStatus {
	    hostId: string;
	    enabled: boolean;
	    active: boolean;
	    path?: string;
	    directory: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new SSHSessionLogStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.enabled = source["enabled"];
	        this.active = source["active"];
	        this.path = source["path"];
	        this.directory = source["directory"];
	        this.error = source["error"];
	    }
	}
	export class SecurityVerifyResult {
	    success: boolean;
	    message: string;
	    retryAfterSeconds: number;
	    remainingAttempts: number;
	
	    static createFrom(source: any = {}) {
	        return new SecurityVerifyResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	        this.retryAfterSeconds = source["retryAfterSeconds"];
	        this.remainingAttempts = source["remainingAttempts"];
	    }
	}
	
	export class TerminalStartResult {
	    connected: boolean;
	    errorCode?: string;
	    error?: string;
	    passwordPrompted?: boolean;
	    passwordSaved?: boolean;
	    warning?: string;
	
	    static createFrom(source: any = {}) {
	        return new TerminalStartResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.connected = source["connected"];
	        this.errorCode = source["errorCode"];
	        this.error = source["error"];
	        this.passwordPrompted = source["passwordPrompted"];
	        this.passwordSaved = source["passwordSaved"];
	        this.warning = source["warning"];
	    }
	}

}

export namespace quickconnect {
	
	export class Candidate {
	    id: string;
	    scheme: string;
	    name: string;
	    host: string;
	    port: number;
	    user: string;
	    hasPassword: boolean;
	    passwordHint: string;
	    recommended: boolean;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new Candidate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.scheme = source["scheme"];
	        this.name = source["name"];
	        this.host = source["host"];
	        this.port = source["port"];
	        this.user = source["user"];
	        this.hasPassword = source["hasPassword"];
	        this.passwordHint = source["passwordHint"];
	        this.recommended = source["recommended"];
	        this.reason = source["reason"];
	    }
	}
	export class SSHConfigHost {
	    alias: string;
	    hostName: string;
	    user: string;
	    port: number;
	    identityFile: string;
	
	    static createFrom(source: any = {}) {
	        return new SSHConfigHost(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.alias = source["alias"];
	        this.hostName = source["hostName"];
	        this.user = source["user"];
	        this.port = source["port"];
	        this.identityFile = source["identityFile"];
	    }
	}

}

export namespace remote {
	
	export class FileInfo {
	    name: string;
	    path: string;
	    size: number;
	    mode: string;
	    modified: string;
	    isDir: boolean;
	    isLink: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FileInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.size = source["size"];
	        this.mode = source["mode"];
	        this.modified = source["modified"];
	        this.isDir = source["isDir"];
	        this.isLink = source["isLink"];
	    }
	}
	export class Host {
	    id: string;
	    name: string;
	    group: string;
	    country: string;
	    countryManual: boolean;
	    vpsAlias: string;
	    vpsDomain: string;
	    vpsMiddleDomain: string;
	    address: string;
	    username: string;
	    password: string;
	    keyPath: string;
	    tags: string;
	    remark: string;
	    systemVersion: string;
	    protocol: string;
	    authType: string;
	    keyPassphrase: string;
	    mfaCode: string;
	    encoding: string;
	    credentialId: string;
	    keyContent: string;
	    hostKey?: string;
	    proxyHost: string;
	    proxyUsername: string;
	    proxyPassword: string;
	    port: number;
	    proxyPort: number;
	    proxyEnabled: boolean;
	    rdpMapDrives: boolean;
	    rdpResolution: string;
	    rdpDisplayMode?: string;
	    rdpSmartSizing?: boolean;
	    rdpUseMultimon?: boolean;
	    rdpDriveIds?: string[];
	    rdpRedirectClipboard?: boolean;
	    rdpAudioPlayback?: number;
	    rdpAudioCapture?: boolean;
	    rdpRedirectPrinters?: boolean;
	    rdpGatewayAddress?: string;
	    rdpGatewayUsername?: string;
	    rdpGatewayPassword?: string;
	    rdpGatewayUsage?: number;
	    rdpJumpHostId?: string;
	    sshJumpHostId?: string;
	    timeout: number;
	    keepAlive: number;
	    starred: boolean;
	    deleted: boolean;
	    lastUsed: string;
	    temporary?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Host(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.group = source["group"];
	        this.country = source["country"];
	        this.countryManual = source["countryManual"];
	        this.vpsAlias = source["vpsAlias"];
	        this.vpsDomain = source["vpsDomain"];
	        this.vpsMiddleDomain = source["vpsMiddleDomain"];
	        this.address = source["address"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.keyPath = source["keyPath"];
	        this.tags = source["tags"];
	        this.remark = source["remark"];
	        this.systemVersion = source["systemVersion"];
	        this.protocol = source["protocol"];
	        this.authType = source["authType"];
	        this.keyPassphrase = source["keyPassphrase"];
	        this.mfaCode = source["mfaCode"];
	        this.encoding = source["encoding"];
	        this.credentialId = source["credentialId"];
	        this.keyContent = source["keyContent"];
	        this.hostKey = source["hostKey"];
	        this.proxyHost = source["proxyHost"];
	        this.proxyUsername = source["proxyUsername"];
	        this.proxyPassword = source["proxyPassword"];
	        this.port = source["port"];
	        this.proxyPort = source["proxyPort"];
	        this.proxyEnabled = source["proxyEnabled"];
	        this.rdpMapDrives = source["rdpMapDrives"];
	        this.rdpResolution = source["rdpResolution"];
	        this.rdpDisplayMode = source["rdpDisplayMode"];
	        this.rdpSmartSizing = source["rdpSmartSizing"];
	        this.rdpUseMultimon = source["rdpUseMultimon"];
	        this.rdpDriveIds = source["rdpDriveIds"];
	        this.rdpRedirectClipboard = source["rdpRedirectClipboard"];
	        this.rdpAudioPlayback = source["rdpAudioPlayback"];
	        this.rdpAudioCapture = source["rdpAudioCapture"];
	        this.rdpRedirectPrinters = source["rdpRedirectPrinters"];
	        this.rdpGatewayAddress = source["rdpGatewayAddress"];
	        this.rdpGatewayUsername = source["rdpGatewayUsername"];
	        this.rdpGatewayPassword = source["rdpGatewayPassword"];
	        this.rdpGatewayUsage = source["rdpGatewayUsage"];
	        this.rdpJumpHostId = source["rdpJumpHostId"];
	        this.sshJumpHostId = source["sshJumpHostId"];
	        this.timeout = source["timeout"];
	        this.keepAlive = source["keepAlive"];
	        this.starred = source["starred"];
	        this.deleted = source["deleted"];
	        this.lastUsed = source["lastUsed"];
	        this.temporary = source["temporary"];
	    }
	}
	export class Result {
	    output: string;
	    exitCode: number;
	    duration: number;
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.output = source["output"];
	        this.exitCode = source["exitCode"];
	        this.duration = source["duration"];
	    }
	}

}

