import { type ChatContextItem } from '@grafana/assistant';
import { type DataFrame } from '@grafana/data';
export declare function getAssistantContextFromDataFrame(data: DataFrame): ChatContextItem[];
