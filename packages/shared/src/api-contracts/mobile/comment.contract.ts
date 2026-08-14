import type { MobileBffComment } from './bff.contract';

export interface MobileCommentSheetResponse {
    articleId: string;
    title: string;
    subtitle: string;
    total: number;
    comments: MobileBffComment[];
    composer: {
        placeholder: string;
        requiresLogin: boolean;
    };
}

export interface MobileCommentCreateRequest {
    articleId: string;
    parentId?: string;
    content: string;
    source?: string;
}

export interface MobileCommentCreateResponse {
    comment: MobileBffComment;
    total: number;
    pendingModeration?: boolean;
}
