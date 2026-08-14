export interface ApiResponse<T = unknown> {
    success: boolean;
    data: T;
    message?: string;
}

export interface SuccessResponse<T = unknown> extends ApiResponse<T> {
    traceId?: string;
    timestamp?: string;
}

export interface ErrorResponse {
    code: string;
    message: string;
    httpStatus?: number;
    traceId?: string;
    retryable?: boolean;
    timestamp?: string;
    path?: string;
}

export interface PaginatedResponse<T> {
    items: T[];
    total: number;
    page: number;
    pageSize: number;
    hasMore: boolean;
    cursor?: string;
    nextCursor?: string;
}

export interface OffsetPagination {
    page: number;
    limit: number;
    total: number;
    totalPages: number;
}

export interface PaginatedDataResponse<T> {
    data: T[];
    pagination: OffsetPagination;
}

export interface PageDocument {
    id: string;
    title: string;
    name?: string;
    description?: string;
    content?: string;
    createdAt: string;
    updatedAt: string;
    author?: string;
    isPublished?: boolean;
}
